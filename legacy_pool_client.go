package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type LegacyPoolClient struct {
	BaseURL    string
	AdminToken string
	Client     *http.Client
}

type LegacyPoolExportResponse struct {
	Accounts []ExportedAccount `json:"accounts"`
}

func (c *LegacyPoolClient) ExportAccounts() ([]ExportedAccount, error) {
	if c == nil {
		return nil, fmt.Errorf("legacy pool client is not configured")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("legacy pool export base url is empty")
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/v1/admin/pool/export", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AdminToken))

	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("legacy export returned status %d", resp.StatusCode)
	}

	var payload LegacyPoolExportResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Accounts, nil
}
