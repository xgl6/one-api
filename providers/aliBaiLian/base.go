package aliBaiLian

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"one-api/common/requester"
	"one-api/model"
	"one-api/providers/base"
	"one-api/types"
)

// 定义供应商工厂
type AliBaiLianProviderFactory struct{}

type AliBaiLianProvider struct {
	base.BaseProvider
}

// 创建 AliBaiLianProvider
// https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation
func (f AliBaiLianProviderFactory) Create(channel *model.Channel) base.ProviderInterface {
	return &AliBaiLianProvider{
		BaseProvider: base.BaseProvider{
			Config:    getConfig(),
			Channel:   channel,
			Requester: requester.NewHTTPRequester(*channel.Proxy, requestErrorHandle),
		},
	}
}

func getConfig() base.ProviderConfig {
	return base.ProviderConfig{
		BaseURL:         "https://dashscope.aliyuncs.com",
		ChatCompletions: "/api/v1/services/aigc/text-generation/generation",
		Embeddings:      "/api/v1/services/embeddings/text-embedding/text-embedding",
	}
}

// 请求错误处理
func requestErrorHandle(resp *http.Response) *types.OpenAIError {
	AliBaiLianError := &AliBaiLianError{}
	err := json.NewDecoder(resp.Body).Decode(AliBaiLianError)
	if err != nil {
		return nil
	}

	return errorHandle(AliBaiLianError)
}

// 错误处理
func errorHandle(AliBaiLianError *AliBaiLianError) *types.OpenAIError {
	if AliBaiLianError.Code == "" {
		return nil
	}
	return &types.OpenAIError{
		Message: AliBaiLianError.Message,
		Type:    AliBaiLianError.Code,
		Param:   AliBaiLianError.RequestId,
		Code:    AliBaiLianError.Code,
	}
}

func (p *AliBaiLianProvider) GetFullRequestURL(requestURL string, modelName string) string {
	baseURL := strings.TrimSuffix(p.GetBaseURL(), "/")

	if strings.HasPrefix(modelName, "qwen-vl") {
		requestURL = "/api/v1/services/aigc/multimodal-generation/generation"
	}

	return fmt.Sprintf("%s%s", baseURL, requestURL)
}

// 获取请求头
func (p *AliBaiLianProvider) GetRequestHeaders() (headers map[string]string) {
	headers = make(map[string]string)
	p.CommonRequestHeaders(headers)
	headers["Authorization"] = fmt.Sprintf("Bearer %s", p.Channel.Key)
	if p.Channel.Other != "" {
		headers["X-DashScope-Plugin"] = p.Channel.Other
	}

	return headers
}
