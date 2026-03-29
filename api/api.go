package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

const (
	uppercaseChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lowercaseChars = "abcdefghijklmnopqrstuvwxyz"
	numberChars    = "0123456789"
	specialChars   = "!@#$%^&*()-_+="

	allPasswordChars = uppercaseChars + lowercaseChars + numberChars + specialChars

	defaultWebBaseURL          = "https://chataibot.pro"
	defaultAPIBaseURL          = defaultWebBaseURL + "/api"
	defaultSignupPath          = "/app/auth/sign-up?variant=new"
	defaultDistributionChannel = "web"
	defaultAcceptLanguage      = "en"
	defaultBrowserUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
)

type RegisterRequest struct {
	Email                 string `json:"email"`
	Password              string `json:"password"`
	IsAdvertisingAccepted bool   `json:"isAdvertisingAccepted"`
	MainSiteUrl           string `json:"mainSiteUrl"`
	UtmSource             string `json:"utmSource"`
	UtmCampaign           string `json:"utmCampaign"`
	ConnectBusiness       string `json:"connectBusiness"`
	YandexClientId        string `json:"yandexClientId"`
}

type RegisterResponse struct {
	Success bool `json:"success"`
}

type VerifyRequest struct {
	Email           string `json:"email"`
	Token           string `json:"token"`
	ConnectBusiness string `json:"connectBusiness"`
}

type VerifyResponse struct {
	JwtToken string `json:"jwtToken"`
}

type UpdateUserRequest struct {
	Settings map[string]string `json:"settings"`
}

type ChataibotImageResp []struct {
	ImageUrl string `json:"imageUrl"`
}

type ChataibotEditImageResp struct {
	ImageUrl string `json:"imageUrl"`
}

type APIClient struct {
	httpClient          *http.Client
	webBaseURL          string
	apiBaseURL          string
	registrationSpoofIP string
}

func NewAPIClient() *APIClient {
	jar, _ := cookiejar.New(nil)
	return &APIClient{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Jar:     jar,
		},
		webBaseURL: defaultWebBaseURL,
		apiBaseURL: defaultAPIBaseURL,
	}
}

func (c *APIClient) signupURL() string {
	return strings.TrimRight(c.webBaseURL, "/") + defaultSignupPath
}

func (c *APIClient) apiURL(path string) string {
	base := strings.TrimRight(c.apiBaseURL, "/")
	if strings.HasPrefix(path, "/") {
		return base + path
	}
	return base + "/" + path
}

func (c *APIClient) applyCommonBrowserHeaders(req *http.Request) {
	req.Header.Set("Accept-Language", defaultAcceptLanguage)
	req.Header.Set("User-Agent", defaultBrowserUserAgent)
	req.Header.Set("x-distribution-channel", defaultDistributionChannel)
	req.Header.Set("Referer", c.signupURL())
}

func (c *APIClient) ensureRegistrationSpoofIP() string {
	if c == nil {
		return ""
	}
	if strings.TrimSpace(c.registrationSpoofIP) != "" {
		return c.registrationSpoofIP
	}

	firstOctetCandidates := []int64{11, 12, 13, 23, 31, 37, 45, 52, 61, 73, 83, 91, 101, 111, 121, 131, 141, 151, 161, 171, 181, 191, 201, 211, 222}
	firstIdx, err := rand.Int(rand.Reader, big.NewInt(int64(len(firstOctetCandidates))))
	if err != nil {
		c.registrationSpoofIP = "45.66.77.88"
		return c.registrationSpoofIP
	}

	octets := []int64{firstOctetCandidates[firstIdx.Int64()]}
	for range 3 {
		next, err := rand.Int(rand.Reader, big.NewInt(254))
		if err != nil {
			octets = append(octets, 88)
			continue
		}
		octets = append(octets, next.Int64()+1)
	}

	c.registrationSpoofIP = fmt.Sprintf("%d.%d.%d.%d", octets[0], octets[1], octets[2], octets[3])
	return c.registrationSpoofIP
}

func (c *APIClient) applyRegistrationSpoofHeaders(req *http.Request) {
	ip := c.ensureRegistrationSpoofIP()
	if ip == "" {
		return
	}
	req.Header.Set("X-Forwarded-For", ip)
	req.Header.Set("X-Real-IP", ip)
	req.Header.Set("CF-Connecting-IP", ip)
	req.Header.Set("True-Client-IP", ip)
}

func (c *APIClient) primeSignupSession() error {
	req, err := http.NewRequest(http.MethodGet, c.signupURL(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", defaultBrowserUserAgent)
	req.Header.Set("Accept-Language", defaultAcceptLanguage)
	c.applyRegistrationSpoofHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("signup preflight failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// SendRegisterRequest 发送注册请求
func (c *APIClient) SendRegisterRequest(email string) error {
	if err := c.primeSignupSession(); err != nil {
		return fmt.Errorf("预热注册会话失败：%w", err)
	}

	url := c.apiURL("/register")
	password := generateSecurePassword(16)
	payload := RegisterRequest{
		Email:                 email,
		Password:              password,
		IsAdvertisingAccepted: true,
		MainSiteUrl:           strings.TrimRight(c.apiBaseURL, "/"),
		UtmSource:             "",
		UtmCampaign:           "",
		ConnectBusiness:       "",
		YandexClientId:        "",
	}
	fmt.Printf("[*] 生成账号：%s，密码：%s\n", email, password)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("JSON 序列化失败：%w", err)
	}

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	c.applyCommonBrowserHeaders(req)
	c.applyRegistrationSpoofHeaders(req)

	fmt.Printf("[*] 正在向目标网站注册账号：%s...\n", email)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求发送失败：%w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var regResp RegisterResponse
	if err := json.Unmarshal(body, &regResp); err != nil {
		return fmt.Errorf("解析响应失败(HTTP %d)：%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if regResp.Success {
		fmt.Println("[+] 注册请求成功，等待验证码邮件...")
		return nil
	}

	return fmt.Errorf("注册失败(HTTP %d)：%s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// VerifyAccount 验证账号
func (c *APIClient) VerifyAccount(email, code string) (string, error) {
	url := c.apiURL("/register/verify")
	payload := VerifyRequest{
		Email:           email,
		Token:           code,
		ConnectBusiness: "",
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("验证请求序列化失败：%w", err)
	}

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	c.applyCommonBrowserHeaders(req)
	c.applyRegistrationSpoofHeaders(req)

	fmt.Printf("[*] 正在提交验证码 [%s] 激活账号...\n", code)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("验证请求发送失败：%w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var verifyResp VerifyResponse
	if err := json.Unmarshal(body, &verifyResp); err != nil {
		return "", fmt.Errorf("解析 JWT 失败(HTTP %d)：%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if verifyResp.JwtToken != "" {
		fmt.Println("[+] 账号激活成功！成功获取 JWT Token！")
		return verifyResp.JwtToken, nil
	}

	return "", fmt.Errorf("验证失败(HTTP %d)，未返回 Token，服务器响应：%s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// UpdateUserSettings 更新用户设置
func (c *APIClient) UpdateUserSettings(jwtToken, aspectRatio string) bool {
	url := c.apiURL("/user/update")
	payload := UpdateUserRequest{
		Settings: map[string]string{
			"imageAspectRatio": aspectRatio,
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		fmt.Println("[-] 更新设置序列化失败：", err)
		return false
	}

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "token="+jwtToken)
	req.Header.Set("x-distribution-channel", defaultDistributionChannel)
	req.Header.Set("User-Agent", defaultBrowserUserAgent)

	fmt.Printf("[*] 正在设置图片比例为 [%s]...\n", aspectRatio)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Println("[-] 更新设置请求失败：", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true
	}

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("[-] 更新设置失败(HTTP %d)：%s\n", resp.StatusCode, string(body))
	return false
}

// GetCount 获取剩余请求
func (c *APIClient) GetCount(jwtToken string) int {
	url := c.apiURL("/user/answers-count/v2")
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Cookie", "token="+jwtToken)
	req.Header.Set("x-distribution-channel", defaultDistributionChannel)
	req.Header.Set("User-Agent", defaultBrowserUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Println("[-] 获取剩余额度失败：", err)
		return 0
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var respData struct {
		LeftAnswersCount int `json:"leftAnswersCount"`
	}
	if err := json.Unmarshal(body, &respData); err != nil {
		return 0
	}

	return respData.LeftAnswersCount
}

// GenerateImage 图片生成
func (c *APIClient) GenerateImage(prompt, provider, version, jwtToken string) (string, error) {
	url := c.apiURL("/image/generate")
	payload := map[string]any{
		"text":            prompt,
		"from":            1,
		"generationType":  provider,
		"isInternational": true,
	}
	if version != "" {
		payload["version"] = version
	}

	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "token="+jwtToken)
	req.Header.Set("x-distribution-channel", defaultDistributionChannel)
	req.Header.Set("User-Agent", defaultBrowserUserAgent)

	slowClient := *c.httpClient
	slowClient.Timeout = 5 * time.Minute

	fmt.Println("[*] 正在调用模型...")
	resp, err := slowClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("[-] 请求发送失败：%v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", parseUpstreamError(resp.StatusCode, body)
	}

	var imgResp ChataibotImageResp
	if err := json.Unmarshal(body, &imgResp); err != nil {
		return "", fmt.Errorf("解析返回值失败：%s", string(body))
	}

	if len(imgResp) > 0 && imgResp[0].ImageUrl != "" {
		return imgResp[0].ImageUrl, nil
	}

	return "", fmt.Errorf("未能获取到图片链接：%s", string(body))
}

func (c *APIClient) EditImage(prompt, base64Data, model, jwtToken string) (string, error) {
	url := c.apiURL("/file/recognize")

	b64Str := base64Data
	fileName := "upload.png"

	if strings.HasPrefix(b64Str, "data:image/") {
		parts := strings.SplitN(b64Str, ";base64,", 2)
		if len(parts) == 2 {
			if strings.Contains(parts[0], "jpeg") || strings.Contains(parts[0], "jpg") {
				fileName = "upload.jpg"
			}
			b64Str = parts[1]
		}
	}

	imgBytes, err := base64.StdEncoding.DecodeString(b64Str)
	if err != nil {
		return "", fmt.Errorf("Base64 解码失败：%v", err)
	}

	bodyBuffer := &bytes.Buffer{}
	writer := multipart.NewWriter(bodyBuffer)

	_ = writer.WriteField("mode", model)
	_ = writer.WriteField("chatContextId", "-2")
	_ = writer.WriteField("lang", "en")
	_ = writer.WriteField("from", "1")
	_ = writer.WriteField("isInternational", "true")
	_ = writer.WriteField("caption", prompt)

	part, err := writer.CreateFormFile("images", fileName)
	if err != nil {
		return "", fmt.Errorf("创建文件表单失败: %v", err)
	}
	_, err = part.Write(imgBytes)
	if err != nil {
		return "", fmt.Errorf("写入图片数据失败: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return "", fmt.Errorf("关闭 multipart writer 失败: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, url, bodyBuffer)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Cookie", "token="+jwtToken)
	req.Header.Set("x-distribution-channel", defaultDistributionChannel)
	req.Header.Set("User-Agent", defaultBrowserUserAgent)

	slowClient := *c.httpClient
	slowClient.Timeout = 5 * time.Minute

	fmt.Printf("[*] 正在上传图片并执行图生图任务...\n")
	resp, err := slowClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("[-] 请求发送失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", parseUpstreamError(resp.StatusCode, respBody)
	}

	var imgResp ChataibotEditImageResp
	if err := json.Unmarshal(respBody, &imgResp); err != nil {
		return "", fmt.Errorf("解析返回值失败(HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	if imgResp.ImageUrl != "" {
		return imgResp.ImageUrl, nil
	}

	return "", fmt.Errorf("未能获取到编辑后的图片链接: %s", string(respBody))
}

// MergeImage 发送多图合并请求
func (c *APIClient) MergeImage(prompt string, base64Images []string, mergeType, jwtToken string) (string, error) {
	url := c.apiURL("/file/merge")

	bodyBuffer := &bytes.Buffer{}
	writer := multipart.NewWriter(bodyBuffer)

	_ = writer.WriteField("type", mergeType)
	_ = writer.WriteField("lang", "en")
	_ = writer.WriteField("from", "1")
	_ = writer.WriteField("isInternational", "true")
	_ = writer.WriteField("caption", prompt)

	// 循环处理并写入多张图片
	for i, b64Str := range base64Images {
		fileName := fmt.Sprintf("upload_%d.png", i)

		if strings.HasPrefix(b64Str, "data:image/") {
			parts := strings.SplitN(b64Str, ";base64,", 2)
			if len(parts) == 2 {
				if strings.Contains(parts[0], "jpeg") || strings.Contains(parts[0], "jpg") {
					fileName = fmt.Sprintf("upload_%d.jpg", i)
				}
				b64Str = parts[1]
			}
		}

		imgBytes, err := base64.StdEncoding.DecodeString(b64Str)
		if err != nil {
			return "", fmt.Errorf("第 %d 张图片 Base64 解码失败: %v", i+1, err)
		}

		part, err := writer.CreateFormFile("images", fileName)
		if err != nil {
			return "", fmt.Errorf("创建第 %d 张图片表单失败: %v", i+1, err)
		}
		_, err = part.Write(imgBytes)
		if err != nil {
			return "", fmt.Errorf("写入第 %d 张图片数据失败: %v", i+1, err)
		}
	}

	err := writer.Close()
	if err != nil {
		return "", fmt.Errorf("关闭 multipart writer 失败: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, url, bodyBuffer)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Cookie", "token="+jwtToken)
	req.Header.Set("x-distribution-channel", defaultDistributionChannel)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", strings.TrimRight(c.webBaseURL, "/"))
	req.Header.Set("Referer", strings.TrimRight(c.webBaseURL, "/")+"/app/chat?chat_id=-2")
	req.Header.Set("User-Agent", defaultBrowserUserAgent)

	slowClient := *c.httpClient
	slowClient.Timeout = 5 * time.Minute

	fmt.Printf("[*] 正在上传 %d 张图片并执行合并任务...\n", len(base64Images))
	resp, err := slowClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("[-] 请求发送失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", parseUpstreamError(resp.StatusCode, respBody)
	}

	var imgResp ChataibotEditImageResp
	if err := json.Unmarshal(respBody, &imgResp); err != nil {
		return "", fmt.Errorf("解析返回值失败(HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	if imgResp.ImageUrl != "" {
		return imgResp.ImageUrl, nil
	}

	return "", fmt.Errorf("未能获取到合并后的图片链接: %s", string(respBody))
}

func generateSecurePassword(length int) string {
	b := make([]byte, length)
	charSetLength := big.NewInt(int64(len(allPasswordChars)))

	for i := range length {
		randomIndex, err := rand.Int(rand.Reader, charSetLength)
		if err != nil {
			return "a9dkIikaj12"
		}

		b[i] = allPasswordChars[randomIndex.Int64()]
	}

	return string(b)
}
