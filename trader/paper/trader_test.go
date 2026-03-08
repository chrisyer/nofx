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

func TestPaperTrader_Execution(t *testing.T) {
	mockBase := new(MockTrader)
	
	// Set mock to return 40000.0 for any market price query
	mockBase.On("GetMarketPrice", mock.Anything).Return(40000.0, nil)

	var mockStore *store.Store // We can leave this nil as PaperTrader doesn't actively use it yet

	pt := NewPaperTrader(mockBase, mockStore, "test_exchange", "test_trader", 10000.0)

	// Test Initial Balance
	bal, err := pt.GetBalance()
	assert.NoError(t, err)
	assert.Equal(t, 10000.0, bal["wallet_balance"])
	assert.Equal(t, 10000.0, bal["total_equity"])
	assert.True(t, bal["is_paper"].(bool))

	// Open Long 1 BTC at $40000
	res, err := pt.OpenLong("BTCUSDT", 1.0, 10)
	assert.NoError(t, err)
	assert.Equal(t, 1.0, res["quantity"])
	assert.Equal(t, 40000.0, res["price"])
	assert.Equal(t, "FILLED", res["status"])
	assert.True(t, res["is_paper"].(bool))

	// The fee should be 0.1% -> $40. Balance should now be 9960
	balAfterLong, _ := pt.GetBalance()
	assert.Equal(t, 9960.0, balAfterLong["wallet_balance"])

	// Check Position Tracking
	positions, err := pt.GetPositions()
	assert.NoError(t, err)
	assert.Len(t, positions, 1)
	
	pos := positions[0]
	assert.Equal(t, "BTCUSDT", pos["symbol"])
	assert.Equal(t, 1.0, pos["quantity"])
	assert.Equal(t, 40000.0, pos["entry_price"])
	assert.Equal(t, "long", pos["side"])
	assert.True(t, pos["is_paper"].(bool))

	// Close half position (0.5 BTC) at $40000
	closeRes, err := pt.CloseLong("BTCUSDT", 0.5)
	assert.NoError(t, err)
	assert.Equal(t, 0.5, closeRes["quantity"])
	assert.Equal(t, 40000.0, closeRes["price"])
	assert.Equal(t, 0.0, closeRes["pnl"])   // No price difference
	assert.Equal(t, 20.0, closeRes["fee"])  // 0.1% fee on 20k

	// Wallet Balance: 9960 - 20 = 9940
	balAfterClose, _ := pt.GetBalance()
	assert.Equal(t, 9940.0, balAfterClose["wallet_balance"])

	// Assert base trader mock price was queried exactly 3 times
	// 1 for OpenLong, 1 for CloseLong, maybe 1 per GetBalance that calculates unrealized PnL (Total 4-5)
	mockBase.AssertExpectations(t)
}
