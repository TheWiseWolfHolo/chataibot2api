package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"chataibot2api/api"

	"chataibot2api/mail"
)

var mailCFClient *mail.MailCFClient
var apiClient *api.APIClient

type Account struct {
	JWT   string
	Quota int
}

type SimplePool struct {
	newChan  chan *Account
	usedPool []*Account
	maxSize  int
	mu       sync.Mutex
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
	"gpt-image-1.5":          {Provider: "GPT_IMAGE_1_5", Version: "", Cost: 12},
	"gpt-image-1.5-high":     {Provider: "GPT_IMAGE_1_5_HIGH", Version: "", Cost: 40},
	"ideogram":               {Provider: "IDEOGRAM", Version: "", Cost: 8},
	"google-nano-banana-pro": {Provider: "GOOGLE", Version: "nano-banana-pro", Cost: 60},
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

	accountPool := StartPool(cfg.PoolSize)
	fmt.Println("[*] 号池已启动，准备就绪...")

	handler := NewServerHandler(cfg, accountPool)

	fmt.Printf("[*] OpenAI 兼容接口启动在 %d 端口，/v1/images/generations\n", cfg.Port)

	return http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), handler)
}

func StartPool(poolSize int) *SimplePool {
	p := &SimplePool{
		newChan:  make(chan *Account, 10),
		usedPool: make([]*Account, 0, poolSize),
		maxSize:  poolSize,
	}

	for i := 0; i < 3; i++ {
		go func(workerID int) {
			for {
				success, jwt := CreateAccount()
				if success {
					p.newChan <- &Account{JWT: jwt, Quota: 65}
				} else {
					time.Sleep(3 * time.Second)
				}
			}
		}(i)
	}

	return p
}

func (p *SimplePool) Acquire(cost int) *Account {
	p.mu.Lock()

	bestIdx := -1
	for i, acc := range p.usedPool {
		if acc.Quota >= cost {
			if bestIdx == -1 || acc.Quota < p.usedPool[bestIdx].Quota {
				bestIdx = i
			}
		}
	}

	if bestIdx != -1 {
		acc := p.usedPool[bestIdx]
		p.usedPool = append(p.usedPool[:bestIdx], p.usedPool[bestIdx+1:]...)
		p.mu.Unlock()
		return acc
	}
	p.mu.Unlock()

	return <-p.newChan
}

func (p *SimplePool) Release(acc *Account) {
	acc.Quota = apiClient.GetCount(acc.JWT)

	if acc.Quota < 2 {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.usedPool) < p.maxSize {
		p.usedPool = append(p.usedPool, acc)
		return
	}

	minIdx := 0
	for i := 1; i < len(p.usedPool); i++ {
		if p.usedPool[i].Quota < p.usedPool[minIdx].Quota {
			minIdx = i
		}
	}

	if acc.Quota > p.usedPool[minIdx].Quota {
		p.usedPool[minIdx] = acc
	}
}

func ImageHandler(pool *SimplePool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req OpenAIImageReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Model == "" {
			req.Model = "gpt-image-1.5"
		}
		modelCfg, exists := modelRouter[req.Model]
		if !exists {
			http.Error(w, fmt.Sprintf("Unsupported model: %s", req.Model), http.StatusBadRequest)
			return
		}

		isMergeMode := len(req.Images) > 1
		isEditMode := req.Image != "" || len(req.Images) == 1

		// 校验模型是否支持该模式
		if isMergeMode && modelCfg.MergeMode == "" {
			http.Error(w, fmt.Sprintf("Model '%s' does not support image merging", req.Model), http.StatusBadRequest)
			return
		}
		if isEditMode && !isMergeMode && modelCfg.EditMode == "" {
			http.Error(w, fmt.Sprintf("Model '%s' does not support image editing", req.Model), http.StatusBadRequest)
			return
		}

		// 确定需要消耗的额度
		requiredCost := modelCfg.Cost
		if isMergeMode {
			requiredCost = modelCfg.MergeCost
		} else if isEditMode {
			requiredCost = modelCfg.EditCost
		}

		ratio := parseRatio(req.Size)
		acc := pool.Acquire(requiredCost)
		defer pool.Release(acc)

		success := apiClient.UpdateUserSettings(acc.JWT, ratio)
		if !success {
			http.Error(w, "Failed to update user settings", http.StatusInternalServerError)
			return
		}

		var imgURL string
		var err error

		// 路由到对应的方法
		if isMergeMode {
			imgURL, err = apiClient.MergeImage(req.Prompt, req.Images, modelCfg.MergeMode, acc.JWT)
		} else if isEditMode {
			imgData := req.Image
			if imgData == "" {
				imgData = req.Images[0] // 兼容传了 Images 数组但只有一张图的情况
			}
			imgURL, err = apiClient.EditImage(req.Prompt, imgData, modelCfg.EditMode, acc.JWT)
		} else {
			imgURL, err = apiClient.GenerateImage(req.Prompt, modelCfg.Provider, modelCfg.Version, acc.JWT)
		}

		if err != nil {
			http.Error(w, fmt.Sprintf("Generation failed: %v", err), http.StatusInternalServerError)
			return
		}

		resp := OpenAIImageResp{
			Created: time.Now().Unix(),
			Data:    []ImageData{{URL: imgURL}},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
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
