package paper

import (
	"nofx/store"
	"nofx/trader/types"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockTrader is a mock implementation of the types.Trader interface
type MockTrader struct {
	mock.Mock
}

func (m *MockTrader) GetBalance() (map[string]interface{}, error) {
	args := m.Called()
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

func (m *MockTrader) GetPositions() ([]map[string]interface{}, error) {
	args := m.Called()
	return args.Get(0).([]map[string]interface{}), args.Error(1)
}

func (m *MockTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	args := m.Called(symbol, quantity, leverage)
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

func (m *MockTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	args := m.Called(symbol, quantity, leverage)
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

func (m *MockTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	args := m.Called(symbol, quantity)
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

func (m *MockTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	args := m.Called(symbol, quantity)
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

func (m *MockTrader) SetLeverage(symbol string, leverage int) error {
	args := m.Called(symbol, leverage)
	return args.Error(0)
}

func (m *MockTrader) SetMarginMode(symbol string, isCrossMargin bool) error {
	args := m.Called(symbol, isCrossMargin)
	return args.Error(0)
}

func (m *MockTrader) GetMarketPrice(symbol string) (float64, error) {
	args := m.Called(symbol)
	return args.Get(0).(float64), args.Error(1)
}

func (m *MockTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	args := m.Called(symbol, positionSide, quantity, stopPrice)
	return args.Error(0)
}

func (m *MockTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	args := m.Called(symbol, positionSide, quantity, takeProfitPrice)
	return args.Error(0)
}

func (m *MockTrader) CancelStopLossOrders(symbol string) error {
	args := m.Called(symbol)
	return args.Error(0)
}

func (m *MockTrader) CancelTakeProfitOrders(symbol string) error {
	args := m.Called(symbol)
	return args.Error(0)
}

func (m *MockTrader) CancelAllOrders(symbol string) error {
	args := m.Called(symbol)
	return args.Error(0)
}

func (m *MockTrader) CancelStopOrders(symbol string) error {
	args := m.Called(symbol)
	return args.Error(0)
}

func (m *MockTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	args := m.Called(symbol, quantity)
	return args.Get(0).(string), args.Error(1)
}

func (m *MockTrader) GetOrderStatus(symbol string, orderID string) (map[string]interface{}, error) {
	args := m.Called(symbol, orderID)
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

func (m *MockTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
	args := m.Called(startTime, limit)
	return args.Get(0).([]types.ClosedPnLRecord), args.Error(1)
}

func (m *MockTrader) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	args := m.Called(symbol)
	return args.Get(0).([]types.OpenOrder), args.Error(1)
}

func newTestPaperTrader(mockBase *MockTrader, initialBalance float64) *PaperTrader {
	var mockStore *store.Store
	return NewPaperTrader(mockBase, mockStore, "test_exchange", "test_trader", initialBalance)
}

// ========================================================================
// Fix #1: GetPositions field name compatibility
// ========================================================================

func TestPaperTrader_GetPositions_FieldNames(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	_, err := pt.OpenLong("BTCUSDT", 0.5, 10)
	assert.NoError(t, err)

	positions, err := pt.GetPositions()
	assert.NoError(t, err)
	assert.Len(t, positions, 1)

	pos := positions[0]

	// Verify field names match what auto_trader.go expects
	assert.Contains(t, pos, "positionAmt", "Must use 'positionAmt' not 'quantity'")
	assert.Contains(t, pos, "entryPrice", "Must use 'entryPrice' not 'entry_price'")
	assert.Contains(t, pos, "markPrice", "Must use 'markPrice' not 'mark_price'")
	assert.Contains(t, pos, "unRealizedProfit", "Must use 'unRealizedProfit' not 'unrealized_pnl'")
	assert.Contains(t, pos, "liquidationPrice", "Must have 'liquidationPrice'")
	assert.Contains(t, pos, "leverage", "Must have 'leverage'")

	// Verify types
	_, ok := pos["positionAmt"].(float64)
	assert.True(t, ok, "positionAmt must be float64")
	_, ok = pos["leverage"].(float64)
	assert.True(t, ok, "leverage must be float64")

	assert.Equal(t, 0.5, pos["positionAmt"])
	assert.Equal(t, "BTCUSDT", pos["symbol"])
	assert.Equal(t, "long", pos["side"])
}

// ========================================================================
// Fix #2: GetClosedPnL records trades
// ========================================================================

func TestPaperTrader_GetClosedPnL(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	// Open and close a trade
	_, err := pt.OpenLong("BTCUSDT", 0.5, 10)
	assert.NoError(t, err)

	_, err = pt.CloseLong("BTCUSDT", 0.5)
	assert.NoError(t, err)

	// GetClosedPnL should return the closed trade
	trades, err := pt.GetClosedPnL(time.Now().Add(-1*time.Hour), 10)
	assert.NoError(t, err)
	assert.Len(t, trades, 1, "Should have 1 closed trade")

	trade := trades[0]
	assert.Equal(t, "BTCUSDT", trade.Symbol)
	assert.Equal(t, "long", trade.Side)
	assert.Equal(t, 0.5, trade.Quantity)
	assert.NotZero(t, trade.EntryPrice)
	assert.NotZero(t, trade.ExitPrice)
}

// ========================================================================
// Fix #3: Margin/balance check
// ========================================================================

func TestPaperTrader_InsufficientBalance(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	// Very small balance
	pt := newTestPaperTrader(mockBase, 10.0)

	// Try to open a position worth 40000 * 1.0 = $40000 with 10x leverage
	// Margin needed: 40000/10 = $4000 + fee
	_, err := pt.OpenLong("BTCUSDT", 1.0, 10)
	assert.Error(t, err, "Should fail with insufficient balance")
	assert.Contains(t, err.Error(), "insufficient balance")
}

// ========================================================================
// Fix #4: Slippage simulation
// ========================================================================

func TestPaperTrader_Slippage(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	// Open long - should buy at a slightly higher price
	res, err := pt.OpenLong("BTCUSDT", 0.1, 10)
	assert.NoError(t, err)

	buyPrice := res["price"].(float64)
	assert.Greater(t, buyPrice, 40000.0, "Buy price should be higher than market (slippage)")
	assert.Less(t, buyPrice, 40100.0, "Slippage should be small")

	// Close long - should sell at a slightly lower price
	closeRes, err := pt.CloseLong("BTCUSDT", 0.1)
	assert.NoError(t, err)

	sellPrice := closeRes["price"].(float64)
	assert.Less(t, sellPrice, 40000.0, "Sell price should be lower than market (slippage)")
}

// ========================================================================
// Fix #5: SL/TP trigger callback
// ========================================================================

func TestPaperTrader_StopTriggerCallback(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(40000.0, nil).Once()
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(38000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	// Track callback invocations
	var cbSymbol, cbSide, cbReason string
	var cbCalled bool
	pt.SetOnStopTriggered(func(symbol, side, reason string, closePrice, pnl, quantity float64) {
		cbCalled = true
		cbSymbol = symbol
		cbSide = side
		cbReason = reason
	})

	_, _ = pt.OpenLong("BTCUSDT", 0.5, 10)
	_ = pt.SetStopLoss("BTCUSDT", "LONG", 0.5, 39000.0)

	pt.checkStopOrders()

	assert.True(t, cbCalled, "Callback should be invoked")
	assert.Equal(t, "BTCUSDT", cbSymbol)
	assert.Equal(t, "long", cbSide)
	assert.Equal(t, "stop_loss", cbReason)
}

// ========================================================================
// Core execution tests (updated for new field names)
// ========================================================================

func TestPaperTrader_Execution(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	// Test Initial Balance
	bal, err := pt.GetBalance()
	assert.NoError(t, err)
	assert.Equal(t, 100000.0, bal["totalWalletBalance"])
	assert.True(t, bal["isPaper"].(bool))

	// Open Long 1 BTC at ~$40000 (with slippage)
	res, err := pt.OpenLong("BTCUSDT", 1.0, 10)
	assert.NoError(t, err)
	assert.Equal(t, 1.0, res["quantity"])
	assert.Equal(t, "FILLED", res["status"])
	assert.True(t, res["is_paper"].(bool))

	// Check Position Tracking with correct field names
	positions, err := pt.GetPositions()
	assert.NoError(t, err)
	assert.Len(t, positions, 1)

	pos := positions[0]
	assert.Equal(t, "BTCUSDT", pos["symbol"])
	assert.Equal(t, 1.0, pos["positionAmt"])
	assert.Equal(t, "long", pos["side"])
	assert.True(t, pos["is_paper"].(bool))

	// Close position
	_, err = pt.CloseLong("BTCUSDT", 1.0)
	assert.NoError(t, err)

	// No more positions
	positions, _ = pt.GetPositions()
	assert.Len(t, positions, 0)

	mockBase.AssertExpectations(t)
}

func TestPaperTrader_StopLossTriggered(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(40000.0, nil).Once()
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(38000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	_, err := pt.OpenLong("BTCUSDT", 0.5, 10)
	assert.NoError(t, err)

	_ = pt.SetStopLoss("BTCUSDT", "LONG", 0.5, 39000.0)

	pt.checkStopOrders()

	positions, _ := pt.GetPositions()
	assert.Len(t, positions, 0, "Position should be closed by stop loss")

	orders, _ := pt.GetOpenOrders("BTCUSDT")
	assert.Len(t, orders, 0, "Stop orders should be cleared")

	// Verify closed PnL record exists with correct close type
	trades, _ := pt.GetClosedPnL(time.Now().Add(-1*time.Hour), 10)
	assert.Len(t, trades, 1)
	assert.Equal(t, "stop_loss", trades[0].CloseType)
}

func TestPaperTrader_TakeProfitTriggered(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(40000.0, nil).Once()
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(43000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	_, _ = pt.OpenLong("BTCUSDT", 0.5, 10)
	_ = pt.SetTakeProfit("BTCUSDT", "LONG", 0.5, 42000.0)

	pt.checkStopOrders()

	positions, _ := pt.GetPositions()
	assert.Len(t, positions, 0, "Position should be closed by take profit")

	bal, _ := pt.GetBalance()
	walletBal := bal["totalWalletBalance"].(float64)
	assert.Greater(t, walletBal, 100000.0, "Should have profit after TP trigger")
}

func TestPaperTrader_ShortStopLossTriggered(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(40000.0, nil).Once()
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(42000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	_, _ = pt.OpenShort("BTCUSDT", 0.5, 10)
	_ = pt.SetStopLoss("BTCUSDT", "SHORT", 0.5, 41000.0)

	pt.checkStopOrders()

	positions, _ := pt.GetPositions()
	assert.Len(t, positions, 0, "Short position should be closed by stop loss")
}

func TestPaperTrader_CancelStopOrders(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	_ = pt.SetStopLoss("BTCUSDT", "LONG", 0.5, 39000.0)
	_ = pt.SetTakeProfit("BTCUSDT", "LONG", 0.5, 42000.0)

	orders, _ := pt.GetOpenOrders("BTCUSDT")
	assert.Len(t, orders, 2)

	_ = pt.CancelStopLossOrders("BTCUSDT")
	orders, _ = pt.GetOpenOrders("BTCUSDT")
	assert.Len(t, orders, 1, "Only TP remaining")

	_ = pt.CancelAllOrders("BTCUSDT")
	orders, _ = pt.GetOpenOrders("BTCUSDT")
	assert.Len(t, orders, 0)
}

func TestPaperTrader_StopOrderOverwrite(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	_ = pt.SetStopLoss("BTCUSDT", "LONG", 0.5, 39000.0)
	_ = pt.SetStopLoss("BTCUSDT", "LONG", 0.5, 38500.0)

	orders, _ := pt.GetOpenOrders("BTCUSDT")
	assert.Len(t, orders, 1, "Old SL should be overwritten")
	assert.Equal(t, 38500.0, orders[0].StopPrice)
}

func TestPaperTrader_BothSLAndTPCleared(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(40000.0, nil).Once()
	mockBase.On("GetMarketPrice", "BTCUSDT").Return(37000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	_, _ = pt.OpenLong("BTCUSDT", 0.5, 10)
	_ = pt.SetStopLoss("BTCUSDT", "LONG", 0.5, 39000.0)
	_ = pt.SetTakeProfit("BTCUSDT", "LONG", 0.5, 42000.0)

	orders, _ := pt.GetOpenOrders("BTCUSDT")
	assert.Len(t, orders, 2)

	pt.checkStopOrders()

	orders, _ = pt.GetOpenOrders("BTCUSDT")
	assert.Len(t, orders, 0, "Both SL and TP should be cleared")

	positions, _ := pt.GetPositions()
	assert.Len(t, positions, 0)
}

func TestPaperTrader_MarginUsedInBalance(t *testing.T) {
	mockBase := new(MockTrader)
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	pt := newTestPaperTrader(mockBase, 100000.0)

	_, _ = pt.OpenLong("BTCUSDT", 0.5, 10)

	bal, _ := pt.GetBalance()
	marginUsed := bal["marginUsed"].(float64)
	assert.Greater(t, marginUsed, 0.0, "Margin should be used after opening position")

	// margin ≈ 40000 * 0.5 / 10 = 2000
	assert.InDelta(t, 2000.0, marginUsed, 100.0, "Margin should be approximately notional/leverage")

	// Available balance should be reduced by margin
	avail := bal["availableBalance"].(float64)
	walletBal := bal["totalWalletBalance"].(float64)
	assert.Less(t, avail, walletBal, "Available should be less than wallet balance when position is open")
}
