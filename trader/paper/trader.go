package paper

import (
	"errors"
	"fmt"
	"nofx/logger"
	"nofx/store"
	"nofx/trader/types"
	"sync"
	"time"
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
}

// NewPaperTrader creates a new paper trading environment wrapping a real trader
func NewPaperTrader(baseTrader types.Trader, dbStore *store.Store, exchangeID string, traderID string, initialBalance float64) *PaperTrader {
	return &PaperTrader{
		baseTrader:     baseTrader,
		store:          dbStore,
		exchangeID:     exchangeID,
		traderID:       traderID,
		balance:        initialBalance,
		initialBalance: initialBalance,
		positions:      make(map[string]*types.ClosedPnLRecord),
	}
}

// --- Market Data Methods (Delegated to underlying exchange) ---

func (pt *PaperTrader) GetMarketPrice(symbol string) (float64, error) {
	return pt.baseTrader.GetMarketPrice(symbol)
}

func (pt *PaperTrader) GetOrderBook(symbol string, depth int) ([][]float64, [][]float64, error) {
	// If baseTrader supports GetOrderBook, we can use it.
	// But since the standard types.Trader doesn't require it, we might need a type assertion
	if obp, ok := pt.baseTrader.(interface {
		GetOrderBook(string, int) ([][]float64, [][]float64, error)
	}); ok {
		return obp.GetOrderBook(symbol, depth)
	}
	// Fallback to market price
	price, err := pt.GetMarketPrice(symbol)
	if err != nil {
		return nil, nil, err
	}
	// Mock orderbook with spread
	bids := [][]float64{{price * 0.999, 1000}}
	asks := [][]float64{{price * 1.001, 1000}}
	return bids, asks, nil
}

// --- Status and Account Methods (Simulated) ---

func (pt *PaperTrader) GetBalance() (map[string]interface{}, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Calculate unrealized PnL from open mock positions
	unrealizedPnL := 0.0
	for symbol, pos := range pt.positions {
		if pos.Quantity > 0 {
			currentPrice, err := pt.baseTrader.GetMarketPrice(symbol)
			if err == nil && currentPrice > 0 {
				if pos.Side == "long" || pos.Side == "BUY" {
					pnl := (currentPrice - pos.EntryPrice) * pos.Quantity
					unrealizedPnL += pnl
				} else if pos.Side == "short" || pos.Side == "SELL" {
					pnl := (pos.EntryPrice - currentPrice) * pos.Quantity
					unrealizedPnL += pnl
				}
			}
		}
	}

	result := map[string]interface{}{
		"total_equity":      pt.balance + unrealizedPnL,
		"available_balance": pt.balance,
		"wallet_balance":    pt.balance,
		"unrealized_profit": unrealizedPnL,
		"margin_used":       0.0, // Simplified for now
		"is_paper":          true,
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

			// Format position identical to real exchange structs
			p := map[string]interface{}{
				"symbol":         pos.Symbol,
				"side":           pos.Side,
				"quantity":       pos.Quantity,
				"entry_price":    pos.EntryPrice,
				"mark_price":     currentPrice,
				"leverage":       pos.Leverage,
				"unrealized_pnl": unrealized,
				"is_paper":       true,
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

	// Calculate a small slippage/fee for realism (0.1% total cost)
	feeRate := 0.001
	tradeValue := price * quantity
	fee := tradeValue * feeRate
	
	// Deduct fee from balance
	pt.balance -= fee

	// Handle existing position
	posID := fmt.Sprintf("%s_%s", symbol, side)
	existing, exists := pt.positions[posID]
	
	if exists && existing.Quantity > 0 {
		// Average up entry price
		totalVal := (existing.EntryPrice * existing.Quantity) + tradeValue
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

	// Return mock response that looks successful
	return map[string]interface{}{
		"order_id": fmt.Sprintf("paper_%v", time.Now().UnixNano()),
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
	fee := tradeValue * 0.001
	
	// Apply to balance
	pt.balance += pnl - fee

	// Update or remove position
	existing.Quantity -= closeQuantity
	if existing.Quantity <= 0 {
		delete(pt.positions, posID)
	}

	logger.Infof("[PaperTrader] Closed %s %s: qty=%.4f exit_price=%.4f pnl=%.4f fee=%.4f (Balance: %.2f)", side, symbol, closeQuantity, price, pnl, fee, pt.balance)

	// Emit mock response
	return map[string]interface{}{
		"order_id": fmt.Sprintf("paper_close_%v", time.Now().UnixNano()),
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
	logger.Infof("[PaperTrader] Set stop loss for %s %s at %.4f", symbol, positionSide, stopPrice)
	return nil
}

func (pt *PaperTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	logger.Infof("[PaperTrader] Set take profit for %s %s at %.4f", symbol, positionSide, takeProfitPrice)
	return nil
}

func (pt *PaperTrader) CancelStopLossOrders(symbol string) error {
	logger.Infof("[PaperTrader] Canceled stop loss orders for %s", symbol)
	return nil
}

func (pt *PaperTrader) CancelTakeProfitOrders(symbol string) error {
	logger.Infof("[PaperTrader] Canceled take profit orders for %s", symbol)
	return nil
}

func (pt *PaperTrader) CancelAllOrders(symbol string) error {
	logger.Infof("[PaperTrader] Canceled all orders for %s", symbol)
	return nil
}

func (pt *PaperTrader) CancelStopOrders(symbol string) error {
	logger.Infof("[PaperTrader] Canceled stop orders for %s", symbol)
	return nil
}

// --- Status Info Methods ---

func (pt *PaperTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	// Delegate to base trader to format decimal accurately based on exchange limits
	return pt.baseTrader.FormatQuantity(symbol, quantity)
}

func (pt *PaperTrader) GetOrderStatus(symbol string, orderID string) (map[string]interface{}, error) {
	// For paper trading, order fills immediately during OpenLong/CloseLong. 
	// Return true to avoid blocking poll polling routines.
	return map[string]interface{}{
		"status":      "FILLED",
		"avgPrice":    1.0, // Mock details
		"executedQty": 1.0, 
		"commission":  0.0,
	}, nil
}

func (pt *PaperTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
	// Not practically returning real db closed positions here since memory loses it,
	// but can integrate with actual DB if necessary. For now return empty or simple simulated records
	return []types.ClosedPnLRecord{}, nil
}

func (pt *PaperTrader) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	return []types.OpenOrder{}, nil
}
