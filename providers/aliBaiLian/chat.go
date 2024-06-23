package aliBaiLian

import (
	"encoding/json"
	"fmt"
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

	jsonData, err := json.Marshal(aliResponse)
	if err != nil {
		fmt.Println("Error marshaling to JSON:", err)
	}
	fmt.Println("响应:" + string(jsonData))
	fmt.Println("请求阿里云结束======")

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

	fmt.Println("请求阿里云开始======")
	headersStr, err := json.Marshal(headers)
	if err != nil {
		fmt.Println("Error marshaling to JSON:", err)
	}
	fmt.Println("请求头:" + string(headersStr))

	jsonData, err := json.Marshal(aliRequest)
	if err != nil {
		fmt.Println("Error marshaling to JSON:", err)
	}
	fmt.Println("请求参数:" + string(jsonData))

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

	chatChoices := make([]types.ChatCompletionChoice, 0)

	// 添加第一个元素
	choice1 := types.ChatCompletionChoice{
		FinishReason: response.Output.FinishReason,
		Message: types.ChatCompletionMessage{
			Role:    "assistant",
			Content: response.Output.Text,
		},
	}
	chatChoices = append(chatChoices, choice1)

	openaiResponse = &types.ChatCompletionResponse{
		ID:      response.RequestId,
		Object:  "chat.completion",
		Created: utils.GetTimestamp(),
		Model:   request.Model,
		Choices: chatChoices,
		Usage: &types.Usage{
			PromptTokens:     response.Usage.Models[0].InputTokens,
			CompletionTokens: response.Usage.Models[0].OutputTokens,
			TotalTokens:      response.Usage.Models[0].InputTokens + response.Usage.Models[0].OutputTokens,
		},
	}
	jsonData, err := json.Marshal(openaiResponse)
	if err != nil {
		fmt.Println("Error marshaling to JSON:", err)
	}
	fmt.Println("openaiResponse:" + string(jsonData))

	*p.Usage = *openaiResponse.Usage

	return
}

// 阿里云聊天请求体
func (p *AliBaiLianProvider) convertFromChatOpenai(request *types.ChatCompletionRequest) *AliBaiLianChatRequest {
	request.ClearEmptyMessages()

	messagesCount := len(request.Messages)
	message := request.Messages[messagesCount-1].StringContent()

	AliBaiLianChatRequest := &AliBaiLianChatRequest{
		//Model: request.Model,
		Input: AliBaiLianInput{
			Prompt: message,
		},
		Parameters: AliBaiLianParameters{
			ResultFormat:      "message",
			IncrementalOutput: request.Stream,
		},
	}

	return AliBaiLianChatRequest
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
	//content := aliResponse.Output.Choices[0].Message.StringContent()
	content := aliResponse.Output.Text

	var choice types.ChatCompletionStreamChoice
	//choice.Index = aliResponse.Output.Choices[0].Index
	choice.Index = 1
	//choice.Delta.Content = strings.TrimPrefix(content, h.lastStreamResponse)
	choice.Delta.Content = content
	finishReasonStr := aliResponse.Output.FinishReason
	if finishReasonStr != "" {
		if finishReasonStr != "null" {
			finishReason := finishReasonStr
			choice.FinishReason = &finishReason
		}
	}

	if finishReasonStr != "" {
		if finishReasonStr != "null" {
			finishReason := finishReasonStr
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

	if aliResponse.Usage.Models[0].OutputTokens != 0 {
		h.Usage.PromptTokens = aliResponse.Usage.Models[0].InputTokens
		h.Usage.CompletionTokens = aliResponse.Usage.Models[0].OutputTokens
		h.Usage.TotalTokens = aliResponse.Usage.Models[0].InputTokens + aliResponse.Usage.Models[0].OutputTokens
	}

	responseBody, _ := json.Marshal(streamResponse)
	dataChan <- string(responseBody)
}
