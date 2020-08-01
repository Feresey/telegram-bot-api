// Package tgbotapi has functions and types used for interacting with
// the Telegram Bot API.
package tgbotapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/technoweenie/multipartstreamer"
)

type HttpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// BotAPI allows you to interact with the Telegram Bot API.
type BotAPI struct {
	Token  string `json:"token"`
	Buffer int    `json:"buffer"`

	Self            *User      `json:"-"`
	Client          HttpClient `json:"-"`
	shutdownChannel chan interface{}

	apiEndpoint string
}

// NewBotAPI creates a new BotAPI instance.
//
// It requires a token, provided by @BotFather on Telegram.
func NewBotAPI(token string) (*BotAPI, error) {
	return NewBotAPIWithClient(token, APIEndpoint, &http.Client{})
}

// NewBotAPIWithAPIEndpoint creates a new BotAPI instance
// and allows you to pass API endpoint.
//
// It requires a token, provided by @BotFather on Telegram and API endpoint.
func NewBotAPIWithAPIEndpoint(token, apiEndpoint string) (*BotAPI, error) {
	return NewBotAPIWithClient(token, apiEndpoint, &http.Client{})
}

// NewBotAPIWithClient creates a new BotAPI instance
// and allows you to pass a http.Client.
//
// It requires a token, provided by @BotFather on Telegram and API endpoint.
func NewBotAPIWithClient(token, apiEndpoint string, client HttpClient) (*BotAPI, error) {
	bot := &BotAPI{
		Token:           token,
		Client:          client,
		Buffer:          100,
		shutdownChannel: make(chan interface{}),

		apiEndpoint: apiEndpoint,
	}

	self, err := bot.GetMe()
	if err != nil {
		return nil, err
	}

	bot.Self = self

	return bot, nil
}

// SetAPIEndpoint add telegram apiEndpont to Bot
func (bot *BotAPI) SetAPIEndpoint(apiEndpoint string) {
	bot.apiEndpoint = apiEndpoint
}

// MakeRequest makes a request to a specific endpoint with our token.
func (bot *BotAPI) MakeRequest(
	endpoint string,
	params url.Values,
	result interface{},
) (*APIResponse, error) {
	method := fmt.Sprintf(bot.apiEndpoint, bot.Token, endpoint)

	req, err := http.NewRequest("POST", method, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := bot.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := bot.decodeAPIResponse(resp.Body, &apiResp); err != nil {
		return &apiResp, err
	}

	if !apiResp.Ok {
		parameters := ResponseParameters{}
		if apiResp.Parameters != nil {
			parameters = *apiResp.Parameters
		}
		return &apiResp, Error{
			Code:               apiResp.ErrorCode,
			Message:            apiResp.Description,
			ResponseParameters: parameters,
		}
	}

	if result != nil {
		err = json.Unmarshal(apiResp.Result, result)
	}
	return &apiResp, err
}

// decodeAPIResponse decode response and return slice of bytes if debug enabled.
// If debug disabled, just decode http.Response.Body stream to APIResponse struct
// for efficient memory usage
func (bot *BotAPI) decodeAPIResponse(responseBody io.Reader, resp *APIResponse) error {
	return json.NewDecoder(responseBody).Decode(resp)
}

// makeMessageRequest makes a request to a method that returns a Message.
func (bot *BotAPI) makeMessageRequest(endpoint string, params url.Values) (*Message, error) {
	var message Message
	_, err := bot.MakeRequest(endpoint, params, &message)
	return &message, err
}

// UploadFile makes a request to the API with a file.
//
// Requires the parameter to hold the file not be in the params.
// File should be a string to a file path, a FileBytes struct,
// a FileReader struct, or a url.URL.
//
// Note that if your FileReader has a size set to -1, it will read
// the file into memory to calculate a size.
func (bot *BotAPI) UploadFile(
	endpoint string,
	params map[string]string,
	fieldname string,
	file interface{},
) (*APIResponse, error) {
	ms := multipartstreamer.New()

	switch f := file.(type) {
	case string:
		if err := ms.WriteFields(params); err != nil {
			return nil, err
		}

		fileHandle, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		defer fileHandle.Close()

		fi, err := os.Stat(f)
		if err != nil {
			return nil, err
		}

		if err := ms.WriteReader(fieldname, fileHandle.Name(), fi.Size(), fileHandle); err != nil {
			return nil, err
		}
	case FileBytes:
		if err := ms.WriteFields(params); err != nil {
			return nil, err
		}

		buf := bytes.NewBuffer(f.Bytes)
		if err := ms.WriteReader(fieldname, f.Name, int64(len(f.Bytes)), buf); err != nil {
			return nil, err
		}
	case FileReader:
		if err := ms.WriteFields(params); err != nil {
			return nil, err
		}

		if f.Size != -1 {
			if err := ms.WriteReader(fieldname, f.Name, f.Size, f.Reader); err != nil {
				return nil, err
			}

			break
		}

		data, err := ioutil.ReadAll(f.Reader)
		if err != nil {
			return nil, err
		}

		buf := bytes.NewBuffer(data)

		if err := ms.WriteReader(fieldname, f.Name, int64(len(data)), buf); err != nil {
			return nil, err
		}
	case url.URL:
		params[fieldname] = f.String()

		if err := ms.WriteFields(params); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New(ErrBadFileType)
	}

	method := fmt.Sprintf(bot.apiEndpoint, bot.Token, endpoint)

	req, err := http.NewRequest("POST", method, nil)
	if err != nil {
		return nil, err
	}

	ms.SetupRequest(req)

	res, err := bot.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	bytes, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var apiResp APIResponse

	err = json.Unmarshal(bytes, &apiResp)
	if err != nil {
		return nil, err
	}

	if !apiResp.Ok {
		parameters := ResponseParameters{}
		if apiResp.Parameters != nil {
			parameters = *apiResp.Parameters
		}
		return &apiResp, Error{Code: apiResp.ErrorCode, Message: apiResp.Description, ResponseParameters: parameters}
	}

	return &apiResp, nil
}

// GetFileDirectURL returns direct URL to file
//
// It requires the FileID.
func (bot *BotAPI) GetFileDirectURL(fileID string) (string, error) {
	file, err := bot.GetFile(FileConfig{fileID})

	if err != nil {
		return "", err
	}

	return file.Link(bot.Token), nil
}

// GetMe fetches the currently authenticated bot.
//
// This method is called upon creation to validate the token,
// and so you may get this data from BotAPI.Self without the need for
// another request.
func (bot *BotAPI) GetMe() (*User, error) {
	var user User
	_, err := bot.MakeRequest("getMe", nil, &user)
	return &user, err
}

// IsMessageToMe returns true if message directed to this bot.
//
// It requires the Message.
func (bot *BotAPI) IsMessageToMe(message *Message) bool {
	return strings.Contains(message.Text, "@"+bot.Self.UserName)
}

// Send will send a Chattable item to Telegram.
//
// It requires the Chattable to send.
func (bot *BotAPI) Send(c Chattable) (*Message, error) {
	fielable, ok := c.(Fileable)
	if !ok {
		return bot.sendChattable(c)
	}
	return bot.sendFile(fielable)
}

// sendExisting will send a Message with an existing file to Telegram.
func (bot *BotAPI) sendExisting(method string, config Fileable) (*Message, error) {
	v, err := config.values()

	if err != nil {
		return nil, err
	}

	message, err := bot.makeMessageRequest(method, v)
	if err != nil {
		return nil, err
	}

	return message, nil
}

// uploadAndSend will send a Message with a new file to Telegram.
func (bot *BotAPI) uploadAndSend(method string, config Fileable) (*Message, error) {
	params, err := config.params()
	if err != nil {
		return nil, err
	}

	file := config.getFile()

	resp, err := bot.UploadFile(method, params, config.name(), file)
	if err != nil {
		return nil, err
	}

	var message Message
	if err := json.Unmarshal(resp.Result, &message); err != nil {
		return nil, err
	}

	return &message, nil
}

// sendFile determines if the file is using an existing file or uploading
// a new file, then sends it as needed.
func (bot *BotAPI) sendFile(config Fileable) (*Message, error) {
	if config.useExistingFile() {
		return bot.sendExisting(config.method(), config)
	}

	return bot.uploadAndSend(config.method(), config)
}

// sendChattable sends a Chattable.
func (bot *BotAPI) sendChattable(config Chattable) (*Message, error) {
	v, err := config.values()
	if err != nil {
		return nil, err
	}

	message, err := bot.makeMessageRequest(config.method(), v)

	if err != nil {
		return nil, err
	}

	return message, nil
}

// GetUserProfilePhotos gets a user's profile photos.
//
// It requires UserID.
// Offset and Limit are optional.
func (bot *BotAPI) GetUserProfilePhotos(config UserProfilePhotosConfig) (*UserProfilePhotos, error) {
	v := make(url.Values)
	v.Add("user_id", strconv.Itoa(config.UserID))
	if config.Offset != 0 {
		v.Add("offset", strconv.Itoa(config.Offset))
	}
	if config.Limit != 0 {
		v.Add("limit", strconv.Itoa(config.Limit))
	}

	var profilePhotos UserProfilePhotos
	_, err := bot.MakeRequest("getUserProfilePhotos", v, &profilePhotos)
	return &profilePhotos, err
}

// GetFile returns a File which can download a file from Telegram.
//
// Requires FileID.
func (bot *BotAPI) GetFile(config FileConfig) (*File, error) {
	v := url.Values{}
	v.Add("file_id", config.FileID)

	var file File
	_, err := bot.MakeRequest("getFile", v, &file)
	return &file, err
}

// GetUpdates fetches updates.
// If a WebHook is set, this will not return any data!
//
// Offset, Limit, and Timeout are optional.
// To avoid stale items, set Offset to one higher than the previous item.
// Set Timeout to a large number to reduce requests so you can get updates
// instantly instead of having to wait between requests.
func (bot *BotAPI) GetUpdates(config UpdateConfig) ([]Update, error) {
	v := url.Values{}
	if config.Offset != 0 {
		v.Add("offset", strconv.Itoa(config.Offset))
	}
	if config.Limit > 0 {
		v.Add("limit", strconv.Itoa(config.Limit))
	}
	if config.Timeout > 0 {
		v.Add("timeout", strconv.Itoa(config.Timeout))
	}

	var updates []Update
	_, err := bot.MakeRequest("getUpdates", v, &updates)
	return updates, err
}

// RemoveWebhook unsets the webhook.
func (bot *BotAPI) RemoveWebhook() (*APIResponse, error) {
	return bot.MakeRequest("deleteWebhook", url.Values{}, nil)
}

// SetWebhook sets a webhook.
//
// If this is set, GetUpdates will not get any data!
//
// If you do not have a legitimate TLS certificate, you need to include
// your self signed certificate with the config.
func (bot *BotAPI) SetWebhook(config WebhookConfig) (*APIResponse, error) {
	if config.Certificate == nil {
		v := url.Values{}
		v.Add("url", config.URL.String())
		if config.MaxConnections != 0 {
			v.Add("max_connections", strconv.Itoa(config.MaxConnections))
		}

		return bot.MakeRequest("setWebhook", v, nil)
	}

	params := make(map[string]string)
	params["url"] = config.URL.String()
	if config.MaxConnections != 0 {
		params["max_connections"] = strconv.Itoa(config.MaxConnections)
	}

	resp, err := bot.UploadFile("setWebhook", params, "certificate", config.Certificate)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// GetWebhookInfo allows you to fetch information about a webhook and if
// one currently is set, along with pending update count and error messages.
func (bot *BotAPI) GetWebhookInfo() (*WebhookInfo, error) {
	var info WebhookInfo
	_, err := bot.MakeRequest("getWebhookInfo", nil, &info)
	return &info, err
}

// GetUpdatesChan starts and returns a channel for getting updates.
func (bot *BotAPI) GetUpdatesChan(config UpdateConfig) (UpdatesChannel, error) {
	ch := make(chan Update, bot.Buffer)

	go func() {
		defer close(ch)
		for {
			select {
			case <-bot.shutdownChannel:
				close(ch)
				return
			default:
			}

			updates, err := bot.GetUpdates(config)
			if err != nil {
				log.Println(err)
				log.Println("Failed to get updates, retrying in 3 seconds...")
				time.Sleep(time.Second * 3)

				continue
			}

			for _, update := range updates {
				if update.UpdateID >= config.Offset {
					config.Offset = update.UpdateID + 1
					ch <- update
				}
			}
		}
	}()

	return ch, nil
}

// StopReceivingUpdates stops the go routine which receives updates
func (bot *BotAPI) StopReceivingUpdates() {
	close(bot.shutdownChannel)
}

// ListenForWebhook registers a http handler for a webhook.
func (bot *BotAPI) ListenForWebhook(pattern string) UpdatesChannel {
	ch := make(chan Update, bot.Buffer)

	http.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		update, err := bot.HandleUpdate(r)
		if err != nil {
			errMsg, _ := json.Marshal(map[string]string{"error": err.Error()})
			w.WriteHeader(http.StatusBadRequest)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(errMsg)
			return
		}

		ch <- *update
	})

	return ch
}

// HandleUpdate parses and returns update received via webhook
func (bot *BotAPI) HandleUpdate(r *http.Request) (*Update, error) {
	if r.Method != http.MethodPost {
		err := errors.New("wrong HTTP method required POST")
		return nil, err
	}

	var update Update
	err := json.NewDecoder(r.Body).Decode(&update)
	if err != nil {
		return nil, err
	}

	return &update, nil
}

// AnswerInlineQuery sends a response to an inline query.
//
// Note that you must respond to an inline query within 30 seconds.
func (bot *BotAPI) AnswerInlineQuery(config InlineConfig) (*APIResponse, error) {
	v := url.Values{}

	v.Add("inline_query_id", config.InlineQueryID)
	v.Add("cache_time", strconv.Itoa(config.CacheTime))
	v.Add("is_personal", strconv.FormatBool(config.IsPersonal))
	v.Add("next_offset", config.NextOffset)
	data, err := json.Marshal(config.Results)
	if err != nil {
		return nil, err
	}
	v.Add("results", string(data))
	v.Add("switch_pm_text", config.SwitchPMText)
	v.Add("switch_pm_parameter", config.SwitchPMParameter)

	return bot.MakeRequest("answerInlineQuery", v, nil)
}

// AnswerCallbackQuery sends a response to an inline query callback.
func (bot *BotAPI) AnswerCallbackQuery(config CallbackConfig) (*APIResponse, error) {
	v := url.Values{}

	v.Add("callback_query_id", config.CallbackQueryID)
	if config.Text != "" {
		v.Add("text", config.Text)
	}
	v.Add("show_alert", strconv.FormatBool(config.ShowAlert))
	if config.URL != "" {
		v.Add("url", config.URL)
	}
	v.Add("cache_time", strconv.Itoa(config.CacheTime))

	return bot.MakeRequest("answerCallbackQuery", v, nil)
}

// KickChatMember kicks a user from a chat. Note that this only will work
// in supergroups, and requires the bot to be an admin. Also note they
// will be unable to rejoin until they are unbanned.
func (bot *BotAPI) KickChatMember(config KickChatMemberConfig) (*APIResponse, error) {
	v := url.Values{}

	if config.SuperGroupUsername == "" {
		v.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	} else {
		v.Add("chat_id", config.SuperGroupUsername)
	}
	v.Add("user_id", strconv.Itoa(config.UserID))

	if config.UntilDate != 0 {
		v.Add("until_date", strconv.FormatInt(config.UntilDate, 10))
	}

	return bot.MakeRequest("kickChatMember", v, nil)
}

// LeaveChat makes the bot leave the chat.
func (bot *BotAPI) LeaveChat(config ChatConfig) (*APIResponse, error) {
	v := url.Values{}

	if config.SuperGroupUsername == "" {
		v.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	} else {
		v.Add("chat_id", config.SuperGroupUsername)
	}

	return bot.MakeRequest("leaveChat", v, nil)
}

// GetChat gets information about a chat.
func (bot *BotAPI) GetChat(config ChatConfig) (*Chat, error) {
	v := url.Values{}

	if config.SuperGroupUsername == "" {
		v.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	} else {
		v.Add("chat_id", config.SuperGroupUsername)
	}

	var chat Chat
	_, err := bot.MakeRequest("getChat", v, &chat)
	return &chat, err
}

// GetChatAdministrators gets a list of administrators in the chat.
//
// If none have been appointed, only the creator will be returned.
// Bots are not shown, even if they are an administrator.
func (bot *BotAPI) GetChatAdministrators(config ChatConfig) ([]ChatMember, error) {
	v := url.Values{}

	if config.SuperGroupUsername == "" {
		v.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	} else {
		v.Add("chat_id", config.SuperGroupUsername)
	}

	var members []ChatMember
	_, err := bot.MakeRequest("getChatAdministrators", v, &members)
	return members, err
}

// GetChatMembersCount gets the number of users in a chat.
func (bot *BotAPI) GetChatMembersCount(config ChatConfig) (int, error) {
	v := url.Values{}

	if config.SuperGroupUsername == "" {
		v.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	} else {
		v.Add("chat_id", config.SuperGroupUsername)
	}

	var count int
	_, err := bot.MakeRequest("getChatMembersCount", v, &count)
	return count, err
}

// GetChatMember gets a specific chat member.
func (bot *BotAPI) GetChatMember(config ChatConfigWithUser) (*ChatMember, error) {
	v := url.Values{}

	if config.SuperGroupUsername == "" {
		v.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	} else {
		v.Add("chat_id", config.SuperGroupUsername)
	}
	v.Add("user_id", strconv.Itoa(config.UserID))

	var member ChatMember
	_, err := bot.MakeRequest("getChatMember", v, &member)
	return &member, err
}

func chatIDFromChatMemberConfig(config *ChatMemberConfig) string {
	switch {
	case config.SuperGroupUsername != "":
		return config.SuperGroupUsername
	case config.ChannelUsername != "":
		return config.ChannelUsername
	default:
		return strconv.FormatInt(config.ChatID, 10)
	}
}

// UnbanChatMember unbans a user from a chat. Note that this only will work
// in supergroups and channels, and requires the bot to be an admin.
func (bot *BotAPI) UnbanChatMember(config ChatMemberConfig) (*APIResponse, error) {
	v := url.Values{}
	v.Add("chat_id", chatIDFromChatMemberConfig(&config))
	v.Add("user_id", strconv.Itoa(config.UserID))

	return bot.MakeRequest("unbanChatMember", v, nil)
}

// RestrictChatMember to restrict a user in a supergroup. The bot must be an
// administrator in the supergroup for this to work and must have the
// appropriate admin rights. Pass True for all boolean parameters to lift
// restrictions from a user. Returns True on success.
func (bot *BotAPI) RestrictChatMember(config RestrictChatMemberConfig) (*APIResponse, error) {
	v := url.Values{}
	v.Add("chat_id", chatIDFromChatMemberConfig(&config.ChatMemberConfig))
	v.Add("user_id", strconv.Itoa(config.UserID))

	if config.CanSendMessages != nil {
		v.Add("can_send_messages", strconv.FormatBool(*config.CanSendMessages))
	}
	if config.CanSendMediaMessages != nil {
		v.Add("can_send_media_messages", strconv.FormatBool(*config.CanSendMediaMessages))
	}
	if config.CanSendOtherMessages != nil {
		v.Add("can_send_other_messages", strconv.FormatBool(*config.CanSendOtherMessages))
	}
	if config.CanAddWebPagePreviews != nil {
		v.Add("can_add_web_page_previews", strconv.FormatBool(*config.CanAddWebPagePreviews))
	}
	if config.UntilDate != 0 {
		v.Add("until_date", strconv.FormatInt(config.UntilDate, 10))
	}

	return bot.MakeRequest("restrictChatMember", v, nil)
}

// PromoteChatMember add admin rights to user
func (bot *BotAPI) PromoteChatMember(config PromoteChatMemberConfig) (*APIResponse, error) {
	v := url.Values{}
	v.Add("chat_id", chatIDFromChatMemberConfig(&config.ChatMemberConfig))
	v.Add("user_id", strconv.Itoa(config.UserID))

	if config.CanChangeInfo != nil {
		v.Add("can_change_info", strconv.FormatBool(*config.CanChangeInfo))
	}
	if config.CanPostMessages != nil {
		v.Add("can_post_messages", strconv.FormatBool(*config.CanPostMessages))
	}
	if config.CanEditMessages != nil {
		v.Add("can_edit_messages", strconv.FormatBool(*config.CanEditMessages))
	}
	if config.CanDeleteMessages != nil {
		v.Add("can_delete_messages", strconv.FormatBool(*config.CanDeleteMessages))
	}
	if config.CanInviteUsers != nil {
		v.Add("can_invite_users", strconv.FormatBool(*config.CanInviteUsers))
	}
	if config.CanRestrictMembers != nil {
		v.Add("can_restrict_members", strconv.FormatBool(*config.CanRestrictMembers))
	}
	if config.CanPinMessages != nil {
		v.Add("can_pin_messages", strconv.FormatBool(*config.CanPinMessages))
	}
	if config.CanPromoteMembers != nil {
		v.Add("can_promote_members", strconv.FormatBool(*config.CanPromoteMembers))
	}

	return bot.MakeRequest("promoteChatMember", v, nil)
}

// GetGameHighScores allows you to get the high scores for a game.
func (bot *BotAPI) GetGameHighScores(config GetGameHighScoresConfig) ([]GameHighScore, error) {
	v, _ := config.values()

	var highScores []GameHighScore
	_, err := bot.MakeRequest(config.method(), v, &highScores)
	return highScores, err
}

// AnswerShippingQuery allows you to reply to Update with shipping_query parameter.
func (bot *BotAPI) AnswerShippingQuery(config ShippingConfig) (*APIResponse, error) {
	v := url.Values{}

	v.Add("shipping_query_id", config.ShippingQueryID)
	v.Add("ok", strconv.FormatBool(config.OK))
	if config.OK {
		data, err := json.Marshal(config.ShippingOptions)
		if err != nil {
			return nil, err
		}
		v.Add("shipping_options", string(data))
	} else {
		v.Add("error_message", config.ErrorMessage)
	}

	return bot.MakeRequest("answerShippingQuery", v, nil)
}

// AnswerPreCheckoutQuery allows you to reply to Update with pre_checkout_query.
func (bot *BotAPI) AnswerPreCheckoutQuery(config PreCheckoutConfig) (*APIResponse, error) {
	v := url.Values{}

	v.Add("pre_checkout_query_id", config.PreCheckoutQueryID)
	v.Add("ok", strconv.FormatBool(config.OK))
	if !config.OK {
		v.Add("error_message", config.ErrorMessage)
	}

	return bot.MakeRequest("answerPreCheckoutQuery", v, nil)
}

// DeleteMessage deletes a message in a chat
func (bot *BotAPI) DeleteMessage(config DeleteMessageConfig) (*APIResponse, error) {
	v, err := config.values()
	if err != nil {
		return nil, err
	}

	return bot.MakeRequest(config.method(), v, nil)
}

// GetInviteLink get InviteLink for a chat
func (bot *BotAPI) GetInviteLink(config ChatConfig) (string, error) {
	v := url.Values{}

	if config.SuperGroupUsername == "" {
		v.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	} else {
		v.Add("chat_id", config.SuperGroupUsername)
	}

	resp, err := bot.MakeRequest("exportChatInviteLink", v, nil)
	if err != nil {
		return "", err
	}

	var inviteLink string
	err = json.Unmarshal(resp.Result, &inviteLink)

	return inviteLink, err
}

// PinChatMessage pin message in supergroup
func (bot *BotAPI) PinChatMessage(config PinChatMessageConfig) (*APIResponse, error) {
	v, err := config.values()
	if err != nil {
		return nil, err
	}

	return bot.MakeRequest(config.method(), v, nil)
}

// UnpinChatMessage unpin message in supergroup
func (bot *BotAPI) UnpinChatMessage(config UnpinChatMessageConfig) (*APIResponse, error) {
	v, err := config.values()
	if err != nil {
		return nil, err
	}

	return bot.MakeRequest(config.method(), v, nil)
}

// SetChatTitle change title of chat.
func (bot *BotAPI) SetChatTitle(config SetChatTitleConfig) (*APIResponse, error) {
	v, err := config.values()
	if err != nil {
		return nil, err
	}

	return bot.MakeRequest(config.method(), v, nil)
}

// SetChatDescription change description of chat.
func (bot *BotAPI) SetChatDescription(config SetChatDescriptionConfig) (*APIResponse, error) {
	v, err := config.values()
	if err != nil {
		return nil, err
	}

	return bot.MakeRequest(config.method(), v, nil)
}

// SetChatPhoto change photo of chat.
func (bot *BotAPI) SetChatPhoto(config SetChatPhotoConfig) (*APIResponse, error) {
	params, err := config.params()
	if err != nil {
		return nil, err
	}

	file := config.getFile()

	return bot.UploadFile(config.method(), params, config.name(), file)
}

// DeleteChatPhoto delete photo of chat.
func (bot *BotAPI) DeleteChatPhoto(config DeleteChatPhotoConfig) (*APIResponse, error) {
	v, err := config.values()
	if err != nil {
		return nil, err
	}

	return bot.MakeRequest(config.method(), v, nil)
}

// GetStickerSet get a sticker set.
func (bot *BotAPI) GetStickerSet(config GetStickerSetConfig) (*StickerSet, error) {
	v, err := config.values()
	if err != nil {
		return nil, err
	}
	var stickerSet StickerSet
	_, err = bot.MakeRequest(config.method(), v, &stickerSet)
	return &stickerSet, err
}
