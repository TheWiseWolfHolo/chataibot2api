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
	domain     string
	adminToken string
}

func NewMailCFClient(baseUrl, domain, adminToken string) *MailCFClient {
	baseUrl = strings.TrimRight(baseUrl, "/")
	return &MailCFClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseUrl:    baseUrl,
		domain:     domain,
		adminToken: adminToken,
	}
}

// NewMail 创建新邮箱
func (c *MailCFClient) NewMail() string {
	name := generateRandomName()
	payload := map[string]any{
		"enablePrefix": true,
		"name":         name,
		"domain":       c.domain,
	}
	data, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, c.baseUrl+"/admin/new_address", bytes.NewBuffer(data))
	req.Header.Set("x-admin-auth", c.adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Println("[-] 发送请求失败：", err)
		return ""
	}
	defer resp.Body.Close()

	address := fmt.Sprintf("%s@%s", name, c.domain)
	fmt.Printf("[+] 成功创建新邮箱地址：%s\n", address)
	return address
}

// FetchAndExtractCode 拉取邮件并提取验证码
func (c *MailCFClient) FetchAndExtractCode(address string) (bool, string) {
	url := fmt.Sprintf("%s/admin/mails?limit=1&offset=0&address=%s", c.baseUrl, address)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("x-admin-auth", c.adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Println("[-] 拉取邮件失败：", err)
		return false, ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var mailResp MailListResponse
	if err := json.Unmarshal(body, &mailResp); err != nil {
		fmt.Println("[-] 解析 JSON 失败：", err)
		return false, ""
	}

	if len(mailResp.Results) == 0 {
		return true, ""
	}

	rawEmail := mailResp.Results[0].Raw
	re := regexp.MustCompile(`token=(?:3D)?(\d+)`)
	matches := re.FindStringSubmatch(rawEmail)

	if len(matches) > 1 {
		return true, matches[1]
	}

	fmt.Println("[-] 收到了邮件，但未找到验证码，可能格式有变")
	return false, ""
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
