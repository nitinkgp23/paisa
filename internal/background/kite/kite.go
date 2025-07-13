package kite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"github.com/ananthakumaran/paisa/internal/config"
)

// Trade represents a trade from KITE Connect API
type Trade struct {
	TradeID           string          `json:"trade_id"`
	OrderID           string          `json:"order_id"`
	ExchangeOrderID   string          `json:"exchange_order_id"`
	TradingSymbol     string          `json:"tradingsymbol"`
	Exchange          string          `json:"exchange"`
	TransactionType   string          `json:"transaction_type"` // BUY or SELL
	Product           string          `json:"product"`
	AveragePrice      decimal.Decimal `json:"average_price"`
	Quantity          int             `json:"quantity"`
	FillTimestamp     time.Time       `json:"fill_timestamp"`
	ExchangeTimestamp time.Time       `json:"exchange_timestamp"`
}

type DailyTradesTask struct{}

func (t *DailyTradesTask) Name() string {
	return "Daily Trades Fetch"
}

func (t *DailyTradesTask) Schedule() string {
	return "0 16 * * *" // Run at 4 PM daily
}

func (t *DailyTradesTask) ShouldRunOnStartup() bool {
	return true
}

func (t *DailyTradesTask) Run(ctx context.Context, db *gorm.DB) error {
	log.Info("Starting daily trades fetch from KITE Connect")
	// Load KITE configuration
	kiteConfig, err := loadKiteConfig()
	if err != nil {
		return fmt.Errorf("failed to load KITE config: %w", err)
	}

	// Get a valid access token. If the existing access token is expired, it will be refreshed.
	// If a login flow is required, it will be performed and the request token will be stored in the database.
	accessToken, err := GetValidAccessToken(db, kiteConfig.APIKey)
	if err != nil {
		log.Warnf("Failed to get a valid access token: %v", err)
		return fmt.Errorf("failed to get a valid access token: %w", err)
	}

	log.Info("Successfully authenticated with KITE Connect")

	// Fetch trades for today
	trades, err := fetchDailyTrades(ctx, kiteConfig.APIKey, accessToken)
	if err != nil {
		return fmt.Errorf("failed to fetch daily trades: %w", err)
	}

	log.Infof("Trades: %+v", trades)
	if len(trades) == 0 {
		log.Info("No trades found for today")
		return nil
	}

	log.Infof("Found %d trades for today", len(trades))

	// Convert trades to ledger format and save
	// err = saveTradesToLedger(db, trades, today)
	// if err != nil {
	// 	return fmt.Errorf("failed to save trades to ledger: %w", err)
	// }

	log.Infof("Successfully processed %d trades", len(trades))
	return nil
}

// loadKiteConfig loads KITE Connect configuration from the config directory
func loadKiteConfig() (*KiteConfig, error) {
	configDir := config.GetConfigDir()
	kiteConfigPath := filepath.Join(configDir, "kite.yaml")

	// Check if config file exists
	if _, err := os.Stat(kiteConfigPath); os.IsNotExist(err) {
		// Create a template config file
		templateConfig := &KiteConfig{
			APIKey:    "your_api_key_here",
			APISecret: "your_api_secret_here",
			UserID:    "your_user_id_here",
			Password:  "your_password_here",
			TOTPToken: "your_totp_secret_here", // Base32 encoded TOTP secret
		}

		// Generate proper YAML
		yamlData, err := yaml.Marshal(templateConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal template config: %w", err)
		}

		err = os.WriteFile(kiteConfigPath, yamlData, 0600)
		if err != nil {
			return nil, fmt.Errorf("failed to create template config file: %w", err)
		}

		log.Infof("Created template KITE config file at: %s", kiteConfigPath)
		log.Info("Please update the configuration with your KITE Connect credentials")
		return nil, fmt.Errorf("KITE config file created, please update with your credentials")
	}

	// Read existing config
	configData, err := os.ReadFile(kiteConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read KITE config file: %w", err)
	}

	var kiteConfig KiteConfig
	err = yaml.Unmarshal(configData, &kiteConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse KITE config file: %w", err)
	}

	return &kiteConfig, nil
}

// fetchDailyTrades fetches trades for a specific date from KITE Connect API
func fetchDailyTrades(ctx context.Context, apiKey string, accessToken string) ([]Trade, error) {
	// KITE Connect API endpoint for fetching trades
	url := fmt.Sprintf("https://api.kite.trade/portfolio/holdings")

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add authentication headers
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", apiKey, accessToken))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse the response
	var response struct {
		Status string  `json:"status"`
		Data   []Trade `json:"data"`
	}

	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if response.Status != "success" {
		return nil, fmt.Errorf("API returned non-success status: %s", response.Status)
	}

	return response.Data, nil
}

// saveTradesToLedger converts trades to ledger format and saves them
func saveTradesToLedger(db *gorm.DB, trades []Trade, date string) error {
	journalPath := config.GetJournalPath()

	// Read existing journal content
	journalContent, err := os.ReadFile(journalPath)
	if err != nil {
		return fmt.Errorf("failed to read journal file: %w", err)
	}

	// Generate ledger entries for trades
	var ledgerEntries []string
	for _, trade := range trades {
		entry := generateLedgerEntry(trade, date)
		if entry != "" {
			ledgerEntries = append(ledgerEntries, entry)
		}
	}

	if len(ledgerEntries) == 0 {
		log.Info("No valid ledger entries generated from trades")
		return nil
	}

	// Add a header comment for the day's trades
	tradeSection := fmt.Sprintf("\n; KITE Connect Trades - %s\n", date)
	tradeSection += strings.Join(ledgerEntries, "\n\n")
	tradeSection += "\n"

	// Append to journal file
	updatedContent := string(journalContent) + tradeSection
	err = os.WriteFile(journalPath, []byte(updatedContent), 0644)
	if err != nil {
		return fmt.Errorf("failed to write updated journal file: %w", err)
	}

	log.Infof("Added %d trade entries to journal file", len(ledgerEntries))
	return nil
}

// generateLedgerEntry converts a trade to ledger format
func generateLedgerEntry(trade Trade, date string) string {
	// Parse the date
	tradeDate, err := time.Parse("2006-01-02", date)
	if err != nil {
		log.Errorf("Failed to parse trade date %s: %v", date, err)
		return ""
	}

	// Determine transaction type and quantity
	quantity := trade.Quantity
	description := ""

	if trade.TransactionType == "BUY" {
		description = fmt.Sprintf("Purchased %d Shares of %s", quantity, trade.TradingSymbol)
	} else if trade.TransactionType == "SELL" {
		quantity = -quantity
		description = fmt.Sprintf("Sold %d Shares of %s", trade.Quantity, trade.TradingSymbol)
	} else {
		log.Warnf("Unknown transaction type: %s", trade.TransactionType)
		return ""
	}

	// Format the price with 4 decimal places
	price := trade.AveragePrice.Round(4)

	// Generate ledger entry
	entry := fmt.Sprintf("%s %s\n", tradeDate.Format("2006/01/02"), description)
	entry += fmt.Sprintf("    Assets:Stocks:%s\t\t\t%d \"%s\" @ %s INR\n",
		trade.TradingSymbol, quantity, trade.TradingSymbol, price.String())
	entry += "    Assets:Broker:Zerodha"

	return entry
}
