package paper

import (
	"errors"
	"fmt"
	"math"
	"nofx/logger"
	"nofx/store"
	"nofx/trader/types"
	"strings"
	"sync"
	"time"
)

// PendingStopOrder represents a pending stop-loss or take-profit order
type PendingStopOrder struct {
	Symbol       string
	PositionSide string  // "LONG" / "SHORT"
	OrderType    string  // "STOP_LOSS" / "TAKE_PROFIT"
	TriggerPrice float64
	Quantity     float64
	CreatedAt    time.Time
}

// StopTriggerCallback is called when a stop order is triggered
// Parameters: symbol, side (long/short), reason (stop_loss/take_profit), closePrice, pnl, quantity
type StopTriggerCallback func(symbol, side, reason string, closePrice, pnl, quantity float64)

// Slippage and fee constants
const (
	feeRate      = 0.001  // 0.1% trading fee
	slippageRate = 0.0005 // 0.05% slippage
)

// PaperTrader is a wrapper around a real trader that overrides execution methods
// to simulate trading without risking real funds. It fetches real market data
// using the underlying trader, but intercepts all order placement and balance
// modification calls.
type PaperTrader struct {
	baseTrader types.Trader
	store      *store.Store
	exchangeID string
	traderID   string

	// In-memory simulated state
	mu             sync.Mutex
	balance        float64
	initialBalance float64
	positions      map[string]*types.ClosedPnLRecord // Simplified position tracking
	closedTrades   []types.ClosedPnLRecord            // Closed position history

	// Stop order monitoring
	stopOrders      []PendingStopOrder    // Pending stop-loss and take-profit orders
	stopMonitorCh   chan struct{}          // Signal to stop the price monitor goroutine
	monitorWg       sync.WaitGroup        // Wait for monitor goroutine to finish
	monitorOnce     sync.Once             // Ensure monitor is only started once
	onStopTriggered StopTriggerCallback   // Callback when SL/TP triggers
}

// NewPaperTrader creates a new paper trading environment wrapping a real trader
func NewPaperTrader(baseTrader types.Trader, dbStore *store.Store, exchangeID string, traderID string, initialBalance float64) *PaperTrader {
	pt := &PaperTrader{
		baseTrader:     baseTrader,
		store:          dbStore,
		exchangeID:     exchangeID,
		traderID:       traderID,
		balance:        initialBalance,
		initialBalance: initialBalance,
		positions:      make(map[string]*types.ClosedPnLRecord),
	}

	// Restore state from database if available
	pt.restoreFromDB()

	return pt
}

// SetOnStopTriggered sets the callback for when a stop order triggers
func (pt *PaperTrader) SetOnStopTriggered(cb StopTriggerCallback) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.onStopTriggered = cb
}

// --- Market Data Methods (Delegated to underlying exchange) ---

func (pt *PaperTrader) GetMarketPrice(symbol string) (float64, error) {
	return pt.baseTrader.GetMarketPrice(symbol)
}

func (pt *PaperTrader) GetOrderBook(symbol string, depth int) ([][]float64, [][]float64, error) {
	if obp, ok := pt.baseTrader.(interface {
		GetOrderBook(string, int) ([][]float64, [][]float64, error)
	}); ok {
		return obp.GetOrderBook(symbol, depth)
	}
	price, err := pt.GetMarketPrice(symbol)
	if err != nil {
		return nil, nil, err
	}
	bids := [][]float64{{price * 0.999, 1000}}
	asks := [][]float64{{price * 1.001, 1000}}
	return bids, asks, nil
}

// --- Status and Account Methods (Simulated) ---

func (pt *PaperTrader) GetBalance() (map[string]interface{}, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Calculate unrealized PnL and margin used from open positions
	unrealizedPnL := 0.0
	marginUsed := 0.0
	for symbol, pos := range pt.positions {
		if pos.Quantity > 0 {
			currentPrice, err := pt.baseTrader.GetMarketPrice(symbol)
			if err == nil && currentPrice > 0 {
				if pos.Side == "long" || pos.Side == "BUY" {
					unrealizedPnL += (currentPrice - pos.EntryPrice) * pos.Quantity
				} else if pos.Side == "short" || pos.Side == "SELL" {
					unrealizedPnL += (pos.EntryPrice - currentPrice) * pos.Quantity
				}
				// Calculate margin used: notional / leverage
				leverage := pos.Leverage
				if leverage <= 0 {
					leverage = 10
				}
				marginUsed += (currentPrice * pos.Quantity) / float64(leverage)
			}
		}
	}

	result := map[string]interface{}{
		"totalEquity":           pt.balance + unrealizedPnL,
		"availableBalance":      pt.balance - marginUsed,
		"totalWalletBalance":    pt.balance,
		"totalUnrealizedProfit": unrealizedPnL,
		"marginUsed":            marginUsed,
		"isPaper":               true,
	}
	return result, nil
}

func (pt *PaperTrader) GetPositions() ([]map[string]interface{}, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	var result []map[string]interface{}
	for symbol, pos := range pt.positions {
		if pos.Quantity > 0 {
			currentPrice, _ := pt.baseTrader.GetMarketPrice(symbol)
			unrealized := 0.0
			if currentPrice > 0 {
				if pos.Side == "long" || pos.Side == "BUY" {
					unrealized = (currentPrice - pos.EntryPrice) * pos.Quantity
				} else {
					unrealized = (pos.EntryPrice - currentPrice) * pos.Quantity
				}
			}

			leverage := pos.Leverage
			if leverage <= 0 {
				leverage = 10
			}

			// Format position identical to real exchange structs
			// CRITICAL: field names must match what auto_trader.go expects
			p := map[string]interface{}{
				"symbol":            pos.Symbol,
				"side":              pos.Side,
				"positionAmt":       pos.Quantity,       // auto_trader expects "positionAmt"
				"entryPrice":        pos.EntryPrice,     // auto_trader expects "entryPrice"
				"markPrice":         currentPrice,       // auto_trader expects "markPrice"
				"leverage":          float64(leverage),   // auto_trader expects float64
				"unRealizedProfit":  unrealized,          // auto_trader expects "unRealizedProfit"
				"liquidationPrice":  0.0,                 // Paper trading has no liquidation
				"is_paper":          true,
			}
			result = append(result, p)
		}
	}
	return result, nil
}

// --- Execution Methods (Simulated) ---

func (pt *PaperTrader) executeTrade(symbol string, side string, quantity float64, leverage int) (map[string]interface{}, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	price, err := pt.baseTrader.GetMarketPrice(symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get market price for %s: %w", symbol, err)
	}

	// Apply slippage: buy higher, sell lower
	if side == "long" || side == "BUY" {
		price = price * (1 + slippageRate)
	} else {
		price = price * (1 - slippageRate)
	}

	// Calculate fee
	tradeValue := price * quantity
	fee := tradeValue * feeRate

	// Check available balance (margin required + fee)
	if leverage <= 0 {
		leverage = 10
	}
	marginRequired := tradeValue / float64(leverage)
	totalRequired := marginRequired + fee

	if totalRequired > pt.balance {
		return nil, fmt.Errorf("insufficient balance: need %.2f (margin %.2f + fee %.2f), have %.2f",
			totalRequired, marginRequired, fee, pt.balance)
	}

	// Deduct fee from balance
	pt.balance -= fee

	// Handle existing position
	posID := fmt.Sprintf("%s_%s", symbol, side)
	existing, exists := pt.positions[posID]

	if exists && existing.Quantity > 0 {
		// Average up entry price
		totalVal := (existing.EntryPrice * existing.Quantity) + (price * quantity)
		newQty := existing.Quantity + quantity
		existing.EntryPrice = totalVal / newQty
		existing.Quantity = newQty
	} else {
		// Create new position
		pt.positions[posID] = &types.ClosedPnLRecord{
			Symbol:     symbol,
			Side:       side,
			EntryPrice: price,
			Quantity:   quantity,
			Leverage:   leverage,
			EntryTime:  time.Now(),
		}
	}

	logger.Infof("[PaperTrader] Executed %s %s: qty=%.4f price=%.4f fee=%.4f (Balance: %.2f)", side, symbol, quantity, price, fee, pt.balance)

	// Persist to DB
	pt.persistPositionToDB(symbol, side, "open")

	return map[string]interface{}{
		"order_id": fmt.Sprintf("paper_%v", time.Now().UnixNano()),
		"orderId":  fmt.Sprintf("paper_%v", time.Now().UnixNano()),
		"symbol":   symbol,
		"status":   "FILLED",
		"price":    price,
		"quantity": quantity,
		"fee":      fee,
		"side":     side,
		"is_paper": true,
	}, nil
}

func (pt *PaperTrader) closeTrade(symbol string, side string, closeQuantity float64) (map[string]interface{}, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	posID := fmt.Sprintf("%s_%s", symbol, side)
	existing, exists := pt.positions[posID]

	if !exists || existing.Quantity <= 0 {
		return nil, errors.New("no position to close")
	}

	price, err := pt.baseTrader.GetMarketPrice(symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get market price for closing %s: %w", symbol, err)
	}

	// Apply slippage: closing long = sell (lower), closing short = buy (higher)
	if side == "long" || side == "BUY" {
		price = price * (1 - slippageRate)
	} else {
		price = price * (1 + slippageRate)
	}

	if closeQuantity <= 0 || closeQuantity > existing.Quantity {
		closeQuantity = existing.Quantity
	}

	// Calculate PnL
	pnl := 0.0
	if side == "long" || side == "BUY" {
		pnl = (price - existing.EntryPrice) * closeQuantity
	} else {
		pnl = (existing.EntryPrice - price) * closeQuantity
	}

	// Calculate fee
	tradeValue := price * closeQuantity
	fee := tradeValue * feeRate

	// Apply to balance
	pt.balance += pnl - fee

	// Record closed trade
	closeType := "manual"
	pt.closedTrades = append(pt.closedTrades, types.ClosedPnLRecord{
		Symbol:      symbol,
		Side:        side,
		EntryPrice:  existing.EntryPrice,
		ExitPrice:   price,
		Quantity:    closeQuantity,
		RealizedPnL: pnl,
		Fee:         fee,
		Leverage:    existing.Leverage,
		EntryTime:   existing.EntryTime,
		ExitTime:    time.Now(),
		OrderID:     fmt.Sprintf("paper_close_%v", time.Now().UnixNano()),
		CloseType:   closeType,
	})

	// Update or remove position
	existing.Quantity -= closeQuantity
	if existing.Quantity <= 0 {
		delete(pt.positions, posID)
	}

	logger.Infof("[PaperTrader] Closed %s %s: qty=%.4f exit_price=%.4f pnl=%.4f fee=%.4f (Balance: %.2f)", side, symbol, closeQuantity, price, pnl, fee, pt.balance)

	// Persist to DB
	pt.persistPositionToDB(symbol, side, "close")

	return map[string]interface{}{
		"order_id": fmt.Sprintf("paper_close_%v", time.Now().UnixNano()),
		"orderId":  fmt.Sprintf("paper_close_%v", time.Now().UnixNano()),
		"symbol":   symbol,
		"status":   "FILLED",
		"price":    price,
		"quantity": closeQuantity,
		"pnl":      pnl,
		"fee":      fee,
		"side":     side,
		"is_paper": true,
	}, nil
}

func (pt *PaperTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	return pt.executeTrade(symbol, "long", quantity, leverage)
}

func (pt *PaperTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	return pt.executeTrade(symbol, "short", quantity, leverage)
}

func (pt *PaperTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	return pt.closeTrade(symbol, "long", quantity)
}

func (pt *PaperTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	return pt.closeTrade(symbol, "short", quantity)
}

// --- Configuration Methods (Mocked) ---

func (pt *PaperTrader) SetLeverage(symbol string, leverage int) error {
	logger.Infof("[PaperTrader] Set leverage for %s to %d", symbol, leverage)
	return nil
}

func (pt *PaperTrader) SetMarginMode(symbol string, isCrossMargin bool) error {
	logger.Infof("[PaperTrader] Set margin mode for %s to %v", symbol, isCrossMargin)
	return nil
}

func (pt *PaperTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	pt.removeStopOrderLocked(symbol, positionSide, "STOP_LOSS")

	pt.stopOrders = append(pt.stopOrders, PendingStopOrder{
		Symbol:       symbol,
		PositionSide: positionSide,
		OrderType:    "STOP_LOSS",
		TriggerPrice: stopPrice,
		Quantity:     quantity,
		CreatedAt:    time.Now(),
	})

	logger.Infof("[PaperTrader] 📌 Stop loss set for %s %s at %.4f (qty=%.4f)", symbol, positionSide, stopPrice, quantity)
	return nil
}

func (pt *PaperTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	pt.removeStopOrderLocked(symbol, positionSide, "TAKE_PROFIT")

	pt.stopOrders = append(pt.stopOrders, PendingStopOrder{
		Symbol:       symbol,
		PositionSide: positionSide,
		OrderType:    "TAKE_PROFIT",
		TriggerPrice: takeProfitPrice,
		Quantity:     quantity,
		CreatedAt:    time.Now(),
	})

	logger.Infof("[PaperTrader] 📌 Take profit set for %s %s at %.4f (qty=%.4f)", symbol, positionSide, takeProfitPrice, quantity)
	return nil
}

func (pt *PaperTrader) CancelStopLossOrders(symbol string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	count := pt.removeStopOrdersByTypeLocked(symbol, "STOP_LOSS")
	logger.Infof("[PaperTrader] Canceled %d stop loss orders for %s", count, symbol)
	return nil
}

func (pt *PaperTrader) CancelTakeProfitOrders(symbol string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	count := pt.removeStopOrdersByTypeLocked(symbol, "TAKE_PROFIT")
	logger.Infof("[PaperTrader] Canceled %d take profit orders for %s", count, symbol)
	return nil
}

func (pt *PaperTrader) CancelAllOrders(symbol string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	count := pt.removeStopOrdersBySymbolLocked(symbol)
	logger.Infof("[PaperTrader] Canceled %d orders for %s", count, symbol)
	return nil
}

func (pt *PaperTrader) CancelStopOrders(symbol string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	count := pt.removeStopOrdersBySymbolLocked(symbol)
	logger.Infof("[PaperTrader] Canceled %d stop orders for %s", count, symbol)
	return nil
}

// --- Stop Order Helper Methods (must be called with mu held) ---

func (pt *PaperTrader) removeStopOrderLocked(symbol, positionSide, orderType string) {
	filtered := pt.stopOrders[:0]
	for _, o := range pt.stopOrders {
		if !(o.Symbol == symbol && strings.EqualFold(o.PositionSide, positionSide) && o.OrderType == orderType) {
			filtered = append(filtered, o)
		}
	}
	pt.stopOrders = filtered
}

func (pt *PaperTrader) removeStopOrdersByTypeLocked(symbol, orderType string) int {
	count := 0
	filtered := pt.stopOrders[:0]
	for _, o := range pt.stopOrders {
		if o.Symbol == symbol && o.OrderType == orderType {
			count++
		} else {
			filtered = append(filtered, o)
		}
	}
	pt.stopOrders = filtered
	return count
}

func (pt *PaperTrader) removeStopOrdersBySymbolLocked(symbol string) int {
	count := 0
	filtered := pt.stopOrders[:0]
	for _, o := range pt.stopOrders {
		if o.Symbol == symbol {
			count++
		} else {
			filtered = append(filtered, o)
		}
	}
	pt.stopOrders = filtered
	return count
}

func (pt *PaperTrader) removeStopOrdersByPositionLocked(symbol, positionSide string) int {
	count := 0
	filtered := pt.stopOrders[:0]
	for _, o := range pt.stopOrders {
		if o.Symbol == symbol && strings.EqualFold(o.PositionSide, positionSide) {
			count++
		} else {
			filtered = append(filtered, o)
		}
	}
	pt.stopOrders = filtered
	return count
}

// --- Price Monitor ---

// StartPriceMonitor starts the background goroutine that checks stop orders every 5 seconds
func (pt *PaperTrader) StartPriceMonitor() {
	pt.monitorOnce.Do(func() {
		pt.stopMonitorCh = make(chan struct{})
		pt.monitorWg.Add(1)
		go func() {
			defer pt.monitorWg.Done()

			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()

			logger.Info("[PaperTrader] 🔍 Price monitor started (checking every 5s)")

			for {
				select {
				case <-ticker.C:
					pt.checkStopOrders()
				case <-pt.stopMonitorCh:
					logger.Info("[PaperTrader] ⏹ Price monitor stopped")
					return
				}
			}
		}()
	})
}

// StopPriceMonitor stops the background price monitor goroutine
func (pt *PaperTrader) StopPriceMonitor() {
	if pt.stopMonitorCh != nil {
		close(pt.stopMonitorCh)
		pt.monitorWg.Wait()
	}
}

// checkStopOrders checks all pending stop orders against current market prices
func (pt *PaperTrader) checkStopOrders() {
	pt.mu.Lock()
	ordersToCheck := make([]PendingStopOrder, len(pt.stopOrders))
	copy(ordersToCheck, pt.stopOrders)
	pt.mu.Unlock()

	if len(ordersToCheck) == 0 {
		return
	}

	// Collect unique symbols
	symbolSet := make(map[string]bool)
	for _, o := range ordersToCheck {
		symbolSet[o.Symbol] = true
	}

	// Fetch prices for all symbols
	prices := make(map[string]float64)
	for symbol := range symbolSet {
		price, err := pt.baseTrader.GetMarketPrice(symbol)
		if err != nil {
			logger.Infof("[PaperTrader] ⚠️ Failed to get price for %s: %v", symbol, err)
			continue
		}
		prices[symbol] = price
	}

	// Check each order for trigger
	for _, order := range ordersToCheck {
		currentPrice, ok := prices[order.Symbol]
		if !ok {
			continue
		}

		triggered := false
		side := ""
		reason := ""

		switch {
		case order.OrderType == "STOP_LOSS" && strings.EqualFold(order.PositionSide, "LONG"):
			if currentPrice <= order.TriggerPrice {
				triggered = true
				side = "long"
				reason = "stop_loss"
			}
		case order.OrderType == "STOP_LOSS" && strings.EqualFold(order.PositionSide, "SHORT"):
			if currentPrice >= order.TriggerPrice {
				triggered = true
				side = "short"
				reason = "stop_loss"
			}
		case order.OrderType == "TAKE_PROFIT" && strings.EqualFold(order.PositionSide, "LONG"):
			if currentPrice >= order.TriggerPrice {
				triggered = true
				side = "long"
				reason = "take_profit"
			}
		case order.OrderType == "TAKE_PROFIT" && strings.EqualFold(order.PositionSide, "SHORT"):
			if currentPrice <= order.TriggerPrice {
				triggered = true
				side = "short"
				reason = "take_profit"
			}
		}

		if triggered {
			logger.Infof("[PaperTrader] 🚨 %s triggered for %s %s! Price=%.4f, Trigger=%.4f",
				reason, order.Symbol, order.PositionSide, currentPrice, order.TriggerPrice)

			// Execute close trade (closeTrade acquires the lock internally)
			res, err := pt.closeTrade(order.Symbol, side, order.Quantity)
			if err != nil {
				logger.Infof("[PaperTrader] ❌ Failed to execute %s close for %s: %v", reason, order.Symbol, err)
				continue
			}

			// Update the last closed trade's CloseType
			pt.mu.Lock()
			if len(pt.closedTrades) > 0 {
				pt.closedTrades[len(pt.closedTrades)-1].CloseType = reason
			}

			// Remove all stop orders for this symbol+side (both SL and TP)
			removed := pt.removeStopOrdersByPositionLocked(order.Symbol, order.PositionSide)

			// Get callback reference while holding lock
			cb := pt.onStopTriggered
			pt.mu.Unlock()

			logger.Infof("[PaperTrader] ✅ %s executed, removed %d related orders for %s %s",
				reason, removed, order.Symbol, order.PositionSide)

			// Notify auto_trader via callback (outside lock)
			if cb != nil {
				pnl := 0.0
				closePrice := 0.0
				qty := order.Quantity
				if res != nil {
					if p, ok := res["pnl"].(float64); ok {
						pnl = p
					}
					if p, ok := res["price"].(float64); ok {
						closePrice = p
					}
					if q, ok := res["quantity"].(float64); ok {
						qty = q
					}
				}
				cb(order.Symbol, side, reason, closePrice, pnl, qty)
			}
		}
	}
}

// --- Status Info Methods ---

func (pt *PaperTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	return pt.baseTrader.FormatQuantity(symbol, quantity)
}

func (pt *PaperTrader) GetOrderStatus(symbol string, orderID string) (map[string]interface{}, error) {
	return map[string]interface{}{
		"status":      "FILLED",
		"avgPrice":    1.0,
		"executedQty": 1.0,
		"commission":  0.0,
	}, nil
}

func (pt *PaperTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	var result []types.ClosedPnLRecord
	for i := len(pt.closedTrades) - 1; i >= 0 && len(result) < limit; i-- {
		trade := pt.closedTrades[i]
		if trade.ExitTime.After(startTime) || trade.ExitTime.Equal(startTime) {
			result = append(result, trade)
		}
	}
	return result, nil
}

func (pt *PaperTrader) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	var result []types.OpenOrder
	for _, o := range pt.stopOrders {
		if symbol != "" && o.Symbol != symbol {
			continue
		}

		orderType := "STOP_MARKET"
		if o.OrderType == "TAKE_PROFIT" {
			orderType = "TAKE_PROFIT_MARKET"
		}

		side := "SELL"
		if strings.EqualFold(o.PositionSide, "SHORT") {
			side = "BUY"
		}

		result = append(result, types.OpenOrder{
			OrderID:      fmt.Sprintf("paper_stop_%d", o.CreatedAt.UnixNano()),
			Symbol:       o.Symbol,
			Side:         side,
			PositionSide: o.PositionSide,
			Type:         orderType,
			StopPrice:    o.TriggerPrice,
			Quantity:     o.Quantity,
			Status:       "NEW",
		})
	}
	return result, nil
}

// --- Database Persistence ---

// persistPositionToDB saves position state to the database
// Must be called with pt.mu held
func (pt *PaperTrader) persistPositionToDB(symbol, side, action string) {
	if pt.store == nil {
		return
	}

	posStore := pt.store.Position()
	posID := fmt.Sprintf("%s_%s", symbol, side)
	nowMs := time.Now().UnixMilli()

	positionSide := "LONG"
	if side == "short" {
		positionSide = "SHORT"
	}

	switch action {
	case "open":
		pos, exists := pt.positions[posID]
		if !exists {
			return
		}

		// Check if position already exists in DB
		dbPos, _ := posStore.GetOpenPositionBySymbol(pt.traderID, symbol, positionSide)
		if dbPos != nil {
			// Update existing
			_ = posStore.UpdatePositionQuantityAndPrice(dbPos.ID, 0, pos.EntryPrice, 0)
		} else {
			// Create new
			newPos := &store.TraderPosition{
				TraderID:           pt.traderID,
				ExchangeID:         pt.exchangeID,
				ExchangeType:       "paper",
				ExchangePositionID: fmt.Sprintf("paper_%s_%s_%d", symbol, side, nowMs),
				Symbol:             symbol,
				Side:               positionSide,
				Quantity:           pos.Quantity,
				EntryPrice:         pos.EntryPrice,
				EntryOrderID:       fmt.Sprintf("paper_%d", nowMs),
				EntryTime:          nowMs,
				Leverage:           pos.Leverage,
				Status:             "OPEN",
				Source:             "paper_trading",
				CreatedAt:          nowMs,
				UpdatedAt:          nowMs,
			}
			if err := posStore.CreateOpenPosition(newPos); err != nil {
				logger.Infof("[PaperTrader] ⚠️ Failed to persist position to DB: %v", err)
			}
		}

	case "close":
		dbPos, _ := posStore.GetOpenPositionBySymbol(pt.traderID, symbol, positionSide)
		if dbPos == nil {
			return
		}

		// Check if position fully closed
		if _, exists := pt.positions[posID]; !exists {
			// Fully closed
			lastTrade := pt.closedTrades[len(pt.closedTrades)-1]
			_ = posStore.ClosePositionFully(
				dbPos.ID,
				lastTrade.ExitPrice,
				lastTrade.OrderID,
				nowMs,
				lastTrade.RealizedPnL,
				lastTrade.Fee,
				lastTrade.CloseType,
			)
		}
	}
}

// restoreFromDB restores positions from database on startup
func (pt *PaperTrader) restoreFromDB() {
	if pt.store == nil {
		return
	}

	posStore := pt.store.Position()
	dbPositions, err := posStore.GetOpenPositions(pt.traderID)
	if err != nil {
		logger.Infof("[PaperTrader] ⚠️ Failed to restore positions from DB: %v", err)
		return
	}

	restoredCount := 0
	for _, dbPos := range dbPositions {
		if dbPos.Source != "paper_trading" && dbPos.Source != "snapshot" {
			continue
		}

		side := "long"
		if dbPos.Side == "SHORT" {
			side = "short"
		}

		posID := fmt.Sprintf("%s_%s", dbPos.Symbol, side)
		pt.positions[posID] = &types.ClosedPnLRecord{
			Symbol:     dbPos.Symbol,
			Side:       side,
			EntryPrice: dbPos.EntryPrice,
			Quantity:   math.Abs(dbPos.Quantity),
			Leverage:   dbPos.Leverage,
			EntryTime:  time.UnixMilli(dbPos.EntryTime),
		}
		restoredCount++
	}

	if restoredCount > 0 {
		logger.Infof("[PaperTrader] ♻️ Restored %d positions from database", restoredCount)
	}

	// Recalculate paper balance based on historical closed positions
	var stats struct {
		TotalPnL float64
		TotalFee float64
	}
	err = pt.store.GormDB().Model(&store.TraderPosition{}).
		Select("COALESCE(SUM(realized_pnl), 0) as total_pn_l, COALESCE(SUM(fee), 0) as total_fee").
		Where("trader_id = ? AND source = 'paper_trading' AND status = 'CLOSED'", pt.traderID).
		Scan(&stats).Error

	if err == nil {
		pt.balance += stats.TotalPnL - stats.TotalFee
		logger.Infof("[PaperTrader] 💰 Restored paper balance: %.2f (Initial: %.2f, PnL: %.2f, Fee: %.2f)",
			pt.balance, pt.initialBalance, stats.TotalPnL, stats.TotalFee)
	} else {
		logger.Infof("[PaperTrader] ⚠️ Failed to restore paper balance from DB: %v", err)
	}
}
