package mail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	lowercaseLetters = "abcdefghijklmnopqrstuvwxyz"
	digits           = "0123456789"
)

type MailListResponse struct {
	Results []struct {
		Raw string `json:"raw"`
	} `json:"results"`
}

type MailCFClient struct {
	httpClient *http.Client
	baseUrl    string
	domains    []string
	adminToken string
	mu         sync.Mutex
	nextDomain int
}

func NewMailCFClient(baseUrl string, domains []string, adminToken string) *MailCFClient {
	baseUrl = strings.TrimRight(baseUrl, "/")
	return &MailCFClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseUrl:    baseUrl,
		domains:    append([]string(nil), domains...),
		adminToken: adminToken,
	}
}

// NewMail 创建新邮箱
func (c *MailCFClient) NewMail() (string, error) {
	domains := c.nextDomainOrder()
	if len(domains) == 0 {
		return "", fmt.Errorf("未配置可用邮箱域名")
	}

	name := generateRandomName()
	type domainAttempt struct {
		domain string
		err    error
	}
	attempts := make([]domainAttempt, 0, len(domains))

	for _, domain := range domains {
		payload := map[string]any{
			"enablePrefix": true,
			"name":         name,
			"domain":       domain,
		}
		data, _ := json.Marshal(payload)

		req, _ := http.NewRequest(http.MethodPost, c.baseUrl+"/admin/new_address", bytes.NewBuffer(data))
		req.Header.Set("x-admin-auth", c.adminToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			attempts = append(attempts, domainAttempt{
				domain: domain,
				err:    fmt.Errorf("发送请求失败：%w", err),
			})
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			attempts = append(attempts, domainAttempt{
				domain: domain,
				err:    fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))),
			})
			continue
		}

		address := fmt.Sprintf("%s@%s", name, domain)
		fmt.Printf("[+] 成功创建新邮箱地址：%s\n", address)
		return address, nil
	}

	parts := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.err == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s -> %v", attempt.domain, attempt.err))
	}
	return "", fmt.Errorf("所有邮箱域名创建失败：%s", strings.Join(parts, "; "))
}

// FetchAndExtractCode 拉取邮件并提取验证码
func (c *MailCFClient) FetchAndExtractCode(address string) (bool, string, error) {
	url := fmt.Sprintf("%s/admin/mails?limit=1&offset=0&address=%s", c.baseUrl, address)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("x-admin-auth", c.adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("拉取邮件失败：%w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var mailResp MailListResponse
	if err := json.Unmarshal(body, &mailResp); err != nil {
		return false, "", fmt.Errorf("解析 JSON 失败：%w", err)
	}

	if len(mailResp.Results) == 0 {
		return true, "", nil
	}

	rawEmail := mailResp.Results[0].Raw
	re := regexp.MustCompile(`token=(?:3D)?(\d+)`)
	matches := re.FindStringSubmatch(rawEmail)

	if len(matches) > 1 {
		return true, matches[1], nil
	}

	fmt.Println("[-] 收到了邮件，但未找到验证码，可能格式有变")
	return false, "", nil
}

// DeleteMail 删除邮箱
func (c *MailCFClient) DeleteMail(email string) {
	url := fmt.Sprintf("%s/admin/delete_address/%s", c.baseUrl, email)
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	req.Header.Set("x-admin-auth", c.adminToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Printf("[-] 邮件 %s 删除请求失败：%v\n", email, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		fmt.Printf("[+] 阅后即焚：邮件 %s 已成功删除\n", email)
		return
	}
	fmt.Printf("[-] 邮件 %s 删除失败，状态码：%d\n", email, resp.StatusCode)
}

func generateRandomString(length int, charset string) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

func generateRandomName() string {
	letters1 := generateRandomString(5, lowercaseLetters)
	numCount := rand.Intn(3) + 1
	numbers := generateRandomString(numCount, digits)
	letCount := rand.Intn(3) + 1
	letters2 := generateRandomString(letCount, lowercaseLetters)

	return letters1 + numbers + letters2
}

func (c *MailCFClient) nextDomainOrder() []string {
	if c == nil || len(c.domains) == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	start := c.nextDomain % len(c.domains)
	c.nextDomain = (c.nextDomain + 1) % len(c.domains)

	ordered := make([]string, 0, len(c.domains))
	for i := 0; i < len(c.domains); i++ {
		ordered = append(ordered, c.domains[(start+i)%len(c.domains)])
	}
	return ordered
}
