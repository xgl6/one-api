package aliBaiLian

import (
	"encoding/json"
	"net/http"
	"one-api/common"
	"one-api/common/config"
	"one-api/common/requester"
	"one-api/common/utils"
	"one-api/types"
	"strings"
)

type aliStreamHandler struct {
	Usage              *types.Usage
	Request            *types.ChatCompletionRequest
	lastStreamResponse string
}

func (p *AliBaiLianProvider) CreateChatCompletion(request *types.ChatCompletionRequest) (*types.ChatCompletionResponse, *types.OpenAIErrorWithStatusCode) {
	req, errWithCode := p.getAliBaiLianChatRequest(request)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	aliResponse := &AliChatResponse{}
	// 发送请求
	_, errWithCode = p.Requester.SendRequest(req, aliResponse, false)
	if errWithCode != nil {
		return nil, errWithCode
	}

	return p.convertToChatOpenai(aliResponse, request)
}

func (p *AliBaiLianProvider) CreateChatCompletionStream(request *types.ChatCompletionRequest) (requester.StreamReaderInterface[string], *types.OpenAIErrorWithStatusCode) {
	req, errWithCode := p.getAliBaiLianChatRequest(request)
	if errWithCode != nil {
		return nil, errWithCode
	}
	defer req.Body.Close()

	// 发送请求
	resp, errWithCode := p.Requester.SendRequestRaw(req)
	if errWithCode != nil {
		return nil, errWithCode
	}

	chatHandler := &aliStreamHandler{
		Usage:   p.Usage,
		Request: request,
	}

	return requester.RequestStream[string](p.Requester, resp, chatHandler.handlerStream)
}

func (p *AliBaiLianProvider) getAliBaiLianChatRequest(request *types.ChatCompletionRequest) (*http.Request, *types.OpenAIErrorWithStatusCode) {
	url, errWithCode := p.GetSupportedAPIUri(config.RelayModeChatCompletions)
	if errWithCode != nil {
		return nil, errWithCode
	}
	// 获取请求地址
	fullRequestURL := p.GetFullRequestURL(url, request.Model)

	// 获取请求头
	headers := p.GetRequestHeaders()
	if request.Stream {
		headers["Accept"] = "text/event-stream"
		headers["X-DashScope-SSE"] = "enable"
	}

	aliRequest := p.convertFromChatOpenai(request)
	// 创建请求
	req, err := p.Requester.NewRequest(http.MethodPost, fullRequestURL, p.Requester.WithBody(aliRequest), p.Requester.WithHeader(headers))
	if err != nil {
		return nil, common.ErrorWrapper(err, "new_request_failed", http.StatusInternalServerError)
	}

	return req, nil
}

// 转换为OpenAI聊天请求体
func (p *AliBaiLianProvider) convertToChatOpenai(response *AliChatResponse, request *types.ChatCompletionRequest) (openaiResponse *types.ChatCompletionResponse, errWithCode *types.OpenAIErrorWithStatusCode) {
	aiError := errorHandle(&response.AliBaiLianError)
	if aiError != nil {
		errWithCode = &types.OpenAIErrorWithStatusCode{
			OpenAIError: *aiError,
			StatusCode:  http.StatusBadRequest,
		}
		return
	}

	openaiResponse = &types.ChatCompletionResponse{
		ID:      response.RequestId,
		Object:  "chat.completion",
		Created: utils.GetTimestamp(),
		Model:   request.Model,
		Choices: response.Output.ToChatCompletionChoices(),
		Usage: &types.Usage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
	}

	*p.Usage = *openaiResponse.Usage

	return
}

// 阿里云聊天请求体
func (p *AliBaiLianProvider) convertFromChatOpenai(request *types.ChatCompletionRequest) *AliBaiLianChatRequest {
	request.ClearEmptyMessages()
	messages := make([]AliBaiLianMessage, 0, len(request.Messages))
	for i := 0; i < len(request.Messages); i++ {
		message := request.Messages[i]
		if !strings.HasPrefix(request.Model, "qwen-vl") {
			messages = append(messages, AliBaiLianMessage{
				Content: message.StringContent(),
				Role:    strings.ToLower(message.Role),
			})
		} else {
			openaiContent := message.ParseContent()
			var parts []AliBaiLianMessagePart
			for _, part := range openaiContent {
				if part.Type == types.ContentTypeText {
					parts = append(parts, AliBaiLianMessagePart{
						Text: part.Text,
					})
				} else if part.Type == types.ContentTypeImageURL {
					parts = append(parts, AliBaiLianMessagePart{
						Image: part.ImageURL.URL,
					})
				}
			}
			messages = append(messages, AliBaiLianMessage{
				Content: parts,
				Role:    strings.ToLower(message.Role),
			})
		}

	}

	AliBaiLianChatRequest := &AliBaiLianChatRequest{
		Model: request.Model,
		Input: AliBaiLianInput{
			Messages: messages,
		},
		Parameters: AliBaiLianParameters{
			ResultFormat:      "message",
			IncrementalOutput: request.Stream,
		},
	}

	p.pluginHandle(AliBaiLianChatRequest)

	return AliBaiLianChatRequest
}

func (p *AliBaiLianProvider) pluginHandle(request *AliBaiLianChatRequest) {
	if p.Channel.Plugin == nil {
		return
	}

	plugin := p.Channel.Plugin.Data()

	// 检测是否开启了 web_search 插件
	if pWeb, ok := plugin["web_search"]; ok {
		if enable, ok := pWeb["enable"].(bool); ok && enable {
			request.Parameters.EnableSearch = true
		}
	}
}

// 转换为OpenAI聊天流式请求体
func (h *aliStreamHandler) handlerStream(rawLine *[]byte, dataChan chan string, errChan chan error) {
	// 如果rawLine 前缀不为data:，则直接返回
	if !strings.HasPrefix(string(*rawLine), "data:") {
		*rawLine = nil
		return
	}

	// 去除前缀
	*rawLine = (*rawLine)[5:]

	var aliResponse AliChatResponse
	err := json.Unmarshal(*rawLine, &aliResponse)
	if err != nil {
		errChan <- common.ErrorToOpenAIError(err)
		return
	}

	aiError := errorHandle(&aliResponse.AliBaiLianError)
	if aiError != nil {
		errChan <- aiError
		return
	}

	h.convertToOpenaiStream(&aliResponse, dataChan)

}

func (h *aliStreamHandler) convertToOpenaiStream(aliResponse *AliChatResponse, dataChan chan string) {
	content := aliResponse.Output.Choices[0].Message.StringContent()

	var choice types.ChatCompletionStreamChoice
	choice.Index = aliResponse.Output.Choices[0].Index
	choice.Delta.Content = strings.TrimPrefix(content, h.lastStreamResponse)
	if aliResponse.Output.Choices[0].FinishReason != "" {
		if aliResponse.Output.Choices[0].FinishReason != "null" {
			finishReason := aliResponse.Output.Choices[0].FinishReason
			choice.FinishReason = &finishReason
		}
	}

	if aliResponse.Output.FinishReason != "" {
		if aliResponse.Output.FinishReason != "null" {
			finishReason := aliResponse.Output.FinishReason
			choice.FinishReason = &finishReason
		}
	}

	h.lastStreamResponse = content
	streamResponse := types.ChatCompletionStreamResponse{
		ID:      aliResponse.RequestId,
		Object:  "chat.completion.chunk",
		Created: utils.GetTimestamp(),
		Model:   h.Request.Model,
		Choices: []types.ChatCompletionStreamChoice{choice},
	}

	if aliResponse.Usage.OutputTokens != 0 {
		h.Usage.PromptTokens = aliResponse.Usage.InputTokens
		h.Usage.CompletionTokens = aliResponse.Usage.OutputTokens
		h.Usage.TotalTokens = aliResponse.Usage.InputTokens + aliResponse.Usage.OutputTokens
	}

	responseBody, _ := json.Marshal(streamResponse)
	dataChan <- string(responseBody)
}
