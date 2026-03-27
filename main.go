package main

import (
	"fmt"
	"net/http"
	"os"
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
	GetCount(jwtToken string) int
	SendRegisterRequest(email string) bool
	VerifyAccount(email, code string) string
}

type MailClient interface {
	NewMail() string
	FetchAndExtractCode(address string) (bool, string)
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
	Provider  string
	Version   string
	Cost      int
	EditMode  string
	EditCost  int
	MergeMode string
	MergeCost int
}

var modelRouter = map[string]ModelConfig{
	"gpt-image-1.5":          {Provider: "GPT_IMAGE_1_5", Version: "", Cost: 12, EditMode: "edit_gpt_1_5", EditCost: 12, MergeMode: "merge_gpt_1_5", MergeCost: 12},
	"gpt-image-1.5-high":     {Provider: "GPT_IMAGE_1_5_HIGH", Version: "", Cost: 40, EditMode: "edit_gpt_1_5_high", EditCost: 40, MergeMode: "merge_gpt_1_5_high", MergeCost: 40},
	"ideogram":               {Provider: "IDEOGRAM", Version: "", Cost: 8},
	"google-nano-banana-pro": {Provider: "GOOGLE", Version: "nano-banana-pro", Cost: 60, EditMode: "edit_google_nano_banana_pro", EditCost: 60, MergeMode: "merge_google_nano_banana_pro", MergeCost: 60},
	"google-nano-banana":     {Provider: "GOOGLE", Version: "nano-banana", Cost: 15, EditMode: "edit_google_nano_banana", EditCost: 15, MergeMode: "merge_google_nano_banana", MergeCost: 15},
	"google-nano-banana-2":   {Provider: "GOOGLE", Version: "nano-banana-2", Cost: 30, EditMode: "edit_google_nano_banana_2", EditCost: 30, MergeMode: "merge_google_nano_banana_2", MergeCost: 30},
	"midjourney-7":           {Provider: "MIDJOURNEY", Version: "7", Cost: 20},
	"qwen-lora":              {Provider: "QWEN", Version: "lora", Cost: 2, EditMode: "edit_qwen_lora", EditCost: 2, MergeMode: "merge_qwen_lora", MergeCost: 2},
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

	app := NewApp(accountPool, apiClient, time.Now)
	handler := NewServerHandler(cfg, app)

	fmt.Printf("[*] OpenAI 兼容接口启动在 %d 端口，/v1/images/generations /v1/models /v1/chat/completions\n", cfg.Port)

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

func CreateAccount() (bool, string) {
	if mailCFClient == nil {
		fmt.Println("[-] cloudflare 邮箱客户端未初始化")
		return false, ""
	}

	email := mailCFClient.NewMail()
	if email == "" {
		fmt.Println("[-] 创建 cloudflare 邮箱失败")
		return false, ""
	}

	// 提交注册
	if !apiClient.SendRegisterRequest(email) {
		fmt.Println("[-] 提交注册失败")
		return false, ""
	}

	// 获取邮件验证码
	var mailCode string
	start := time.Now()
	for {
		if time.Since(start) > 60*time.Second {
			fmt.Println("[-] 获取验证码超时")
			return false, ""
		}

		next, code := mailCFClient.FetchAndExtractCode(email)
		if !next {
			fmt.Println("[-] 获取验证码失败")
			return false, ""
		}
		if next && code != "" {
			mailCode = code
			break
		}

		time.Sleep(2 * time.Second)
	}

	// 提交验证码
	jwtToken := apiClient.VerifyAccount(email, mailCode)
	if jwtToken == "" {
		return false, ""
	}
	return true, jwtToken
}
