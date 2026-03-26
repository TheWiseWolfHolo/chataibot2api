package mail

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

type MailTempClient struct {
	httpClient *http.Client
}

func NewMailTempClient() *MailTempClient {
	return &MailTempClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *MailTempClient) NewMail() (string, string) {
	url := "https://api.tempmail.lol/v2/inbox/create"
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Println("[-] 创建 Temp 邮箱失败：", err)
		return "", ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var respData struct {
		Address string `json:"address"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(body, &respData); err != nil {
		fmt.Println("[-] 创建 Temp 邮箱失败：", err)
		return "", ""
	}

	return respData.Address, respData.Token
}

func (c *MailTempClient) FetchAndExtractCode(token string) (bool, string) {
	url := "https://api.tempmail.lol/v2/inbox?token=" + token
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Println("[-] 获取 Temp 邮件失败：", err)
		return false, ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var respData struct {
		Emails []struct {
			Body string `json:"body"`
		} `json:"emails"`
		Expired bool
	}

	if err := json.Unmarshal(body, &respData); err != nil {
		fmt.Println("[-] 解析 Temp 邮件失败")
	}

	if respData.Expired {
		return false, ""
	}

	if len(respData.Emails) < 1 {
		return true, ""
	}

	rawEmail := respData.Emails[0].Body
	re := regexp.MustCompile(`token=(?:3D)?(\d+)`)
	matches := re.FindStringSubmatch(rawEmail)

	if len(matches) > 1 {
		return true, matches[1]
	}

	fmt.Println("[-] 收到了邮件，但未找到验证码，可能格式有变")
	return false, ""
}
