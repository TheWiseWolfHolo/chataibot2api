package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"chataibot2api/api"

	"chataibot2api/mail"
)

type APIClient interface {
	UpdateUserSettings(jwtToken, aspectRatio string) bool
	GenerateImage(prompt, provider, version, jwtToken string) (string, error)
	EditImage(prompt, base64Data, model, jwtToken string) (string, error)
	MergeImage(prompt string, base64Images []string, mergeType, jwtToken string) (string, error)
	CreateChatContext(model, title, jwtToken string) (int, error)
	SendTextMessage(req UpstreamTextMessageRequest, jwtToken string) (TextCompletionResult, error)
	StreamTextMessage(req UpstreamTextMessageRequest, jwtToken string, emit func(TextStreamEvent) error) (TextCompletionResult, error)
	GetCount(jwtToken string) (int, error)
	SendRegisterRequest(email string) error
	VerifyAccount(email, code string) (string, error)
}

type MailClient interface {
	NewMail() (string, error)
	FetchAndExtractCode(address string) (bool, string, error)
}

var mailCFClient MailClient
var apiClient APIClient

type Account struct {
	JWT   string
	Quota int
}

type OpenAIImageReq struct {
	Prompt string   `json:"prompt"`
	Model  string   `json:"model"`
	Size   string   `json:"size"`
	Image  string   `json:"image"`
	Images []string `json:"images"`
}

type OpenAIImageResp struct {
	Created int64       `json:"created"`
	Data    []ImageData `json:"data"`
}

type ImageData struct {
	URL string `json:"url"`
}

type ModelConfig struct {
	Provider   string
	Version    string
	Cost       int
	EditMode   string
	EditCost   int
	MergeMode  string
	MergeCost  int
	MergeCosts map[int]int
	Hidden     bool
}

var modelRouter = map[string]ModelConfig{
	"gpt-image-1.5":          {Provider: "GPT_IMAGE_1_5", Version: "", Cost: 12, EditMode: "edit_gpt_1_5", EditCost: 17, MergeMode: "merge_gpt_1_5", MergeCost: 22, MergeCosts: map[int]int{2: 22, 3: 27, 4: 32}},
	"gpt-image-1.5-high":     {Provider: "GPT_IMAGE_1_5_HIGH", Version: "", Cost: 40, EditMode: "edit_gpt_1_5_high", EditCost: 50, MergeMode: "merge_gpt_1_5_high", MergeCost: 60, MergeCosts: map[int]int{2: 60, 3: 70, 4: 80}},
	"ideogram":               {Provider: "IDEOGRAM", Version: "", Cost: 8},
	"google-nano-banana-pro": {Provider: "GOOGLE", Version: "nano-banana-pro", Cost: 60, EditMode: "edit_google_nano_banana_pro", EditCost: 60, MergeMode: "merge_google_nano_banana_pro", MergeCost: 70, MergeCosts: map[int]int{2: 70, 3: 80, 4: 90}, Hidden: true},
	"google-nano-banana":     {Provider: "GOOGLE", Version: "nano-banana", Cost: 15, EditMode: "edit_google_nano_banana", EditCost: 15, MergeMode: "merge_google_nano_banana", MergeCost: 20, MergeCosts: map[int]int{2: 20, 3: 25, 4: 30}},
	"google-nano-banana-2":   {Provider: "GOOGLE", Version: "nano-banana-2", Cost: 30, EditMode: "edit_google_nano_banana_2", EditCost: 30, MergeMode: "merge_google_nano_banana_2", MergeCost: 40, MergeCosts: map[int]int{2: 40, 3: 50, 4: 60}, Hidden: true},
	"midjourney-7":           {Provider: "MIDJOURNEY", Version: "7", Cost: 20, Hidden: true},
	"qwen-lora":              {Provider: "QWEN", Version: "lora", Cost: 2, EditMode: "edit_qwen_lora", EditCost: 2, MergeMode: "merge_qwen_lora", MergeCost: 2, MergeCosts: map[int]int{2: 2, 3: 2, 4: 2}},
	"bytedance-seedream":     {Provider: "BYTEDANCE", Version: "seedream-5-lite", Cost: 14},
}

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Printf("Server failed: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string) error {
	cfg, err := LoadConfig(args, getenv)
	if err != nil {
		return err
	}

	fmt.Println("[*] 使用 cloudflare 自建邮箱")
	mailCFClient = mail.NewMailCFClient(cfg.MailAPIBaseURL, cfg.MailDomain, cfg.MailAdminToken)
	apiClient = api.NewAPIClient()

	accountPool := StartPool(cfg)
	fmt.Println("[*] 号池已启动，准备就绪...")
	if strings.TrimSpace(cfg.PoolStorePath) != "" {
		fmt.Printf("[*] 号池持久化已启用：%s\n", cfg.PoolStorePath)
	}
	if status := accountPool.Status(); status.PersistenceEnabled && status.RestoreLoaded > 0 {
		fmt.Printf("[*] 已从持久化凭证恢复 %d 个账号\n", status.RestoreLoaded)
	}
	if status := accountPool.Status(); status.LastPersistError != "" {
		fmt.Printf("[!] 号池持久化异常：%s\n", status.LastPersistError)
	}

	app := NewApp(accountPool, apiClient, cfg, time.Now)
	handler := NewServerHandler(cfg, app)

	fmt.Printf("[*] OpenAI 兼容接口启动在 %d 端口，/v1/images/generations /v1/models /v1/chat/completions\n", cfg.Port)
	if strings.TrimSpace(cfg.InstanceName) != "" {
		fmt.Printf("[*] 当前实例：%s\n", cfg.InstanceName)
	}
	if strings.TrimSpace(cfg.PublicBaseURL) != "" {
		fmt.Printf("[*] 对外地址：%s\n", cfg.PublicBaseURL)
	}

	return http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), handler)
}

func parseRatio(size string) string {
	switch size {
	case "1024x1024", "1:1":
		return "1:1"
	case "1024x1792", "9:16":
		return "9:16"
	case "1792x1024", "16:9":
		return "16:9"
	default:
		return "auto"
	}
}

func CreateAccount() (string, error) {
	if mailCFClient == nil {
		return "", fmt.Errorf("cloudflare 邮箱客户端未初始化")
	}

	email, err := mailCFClient.NewMail()
	if err != nil {
		return "", fmt.Errorf("创建 cloudflare 邮箱失败：%w", err)
	}

	registrationClient := api.NewAPIClient()

	// 提交注册
	if err := registrationClient.SendRegisterRequest(email); err != nil {
		return "", fmt.Errorf("提交注册失败：%w", err)
	}

	// 获取邮件验证码
	var mailCode string
	start := time.Now()
	for {
		if time.Since(start) > 60*time.Second {
			return "", fmt.Errorf("获取验证码超时")
		}

		next, code, err := mailCFClient.FetchAndExtractCode(email)
		if err != nil {
			return "", fmt.Errorf("获取验证码失败：%w", err)
		}
		if !next {
			return "", fmt.Errorf("获取验证码失败")
		}
		if next && code != "" {
			mailCode = code
			break
		}

		time.Sleep(2 * time.Second)
	}

	// 提交验证码
	jwtToken, err := registrationClient.VerifyAccount(email, mailCode)
	if err != nil {
		return "", fmt.Errorf("提交验证码失败：%w", err)
	}
	return jwtToken, nil
}
