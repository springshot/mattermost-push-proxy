// Copyright (c) 2015 Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package server

import (
	"fmt"
	"time"
	"regexp"
	"strings"

	"github.com/kyokomi/emoji"
	apns "github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sideshow/apns2/payload"
)

type AppleNotificationServer struct {
	ApplePushSettings ApplePushSettings
	AppleClient       *apns.Client
}

func NewAppleNotificationServer(settings ApplePushSettings) NotificationServer {
	return &AppleNotificationServer{ApplePushSettings: settings}
}

func (me *AppleNotificationServer) Initialize() bool {
	LogInfo(fmt.Sprintf("Initializing apple notification server for type=%v", me.ApplePushSettings.Type))

	if len(me.ApplePushSettings.ApplePushCertPrivate) > 0 {
		appleCert, appleCertErr := certificate.FromPemFile(me.ApplePushSettings.ApplePushCertPrivate, me.ApplePushSettings.ApplePushCertPassword)
		if appleCertErr != nil {
			LogCritical(fmt.Sprintf("Failed to load the apple pem cert err=%v for type=%v", appleCertErr, me.ApplePushSettings.Type))
			return false
		}

		if me.ApplePushSettings.ApplePushUseDevelopment {
			me.AppleClient = apns.NewClient(appleCert).Development()
		} else {
			me.AppleClient = apns.NewClient(appleCert).Production()
		}

		return true
	} else {
		LogError(fmt.Sprintf("Apple push notifications not configured.  Missing ApplePushCertPrivate. for type=%v", me.ApplePushSettings.Type))
		return false
	}
}

func (me *AppleNotificationServer) SendNotification(msg *PushNotification) PushResponse {

	data := payload.NewPayload()
	data.Badge(msg.Badge)

	notification := &apns.Notification{}
	notification.DeviceToken = msg.DeviceId
	notification.Payload = data
	notification.Topic = me.ApplePushSettings.ApplePushTopic

	var pushType = msg.Type
	if msg.Type != PUSH_TYPE_CLEAR {
		pushType = PUSH_TYPE_MESSAGE
		data.Category(msg.Category)
		data.Sound("default")
		data.Custom("version", msg.Version)
		data.MutableContent()

		// Springshot - replace mentions in a message from @username{{Full Name}} to @Full Name
		var message = msg.Message
		var mention_regex = regexp.MustCompile(`@[\w\d._-]+{{[^}]*}}`)
		var mention_matches = mention_regex.FindAllString(message, -1)
		LogInfo(fmt.Sprintf("Springshot:: Mention matches = %v", mention_matches))

		for i := 0; i < len(mention_matches); i++ {
			var matched = mention_matches[i]
			var first_split = strings.Split(matched, "{{")
			var second_split = strings.Split(first_split[1], "}}")
			var full_name = strings.Trim(second_split[0], " ")
			message = strings.ReplaceAll(message, matched, "@"+full_name)
		}

		// Springshot - Call notification messages needs to be parsed into differrent attributes
		var message_split = strings.Split(message, "springshot_call=")
		if len(message_split) > 1 { // This is a call message
			data.Sound("call_start.mp3")
			LogInfo(fmt.Sprintf("Springshot:: Call attributes = %v", message_split[1]))
			message = message_split[0]
			var call_attributes = strings.Split(message_split[1], "|")

			data.Custom("call_id", call_attributes[0])
			data.Custom("caller_name", call_attributes[1])
			data.Custom("group_name", call_attributes[2])
			data.Custom("avatar", call_attributes[3])
			LogInfo(fmt.Sprintf("Springshot:: Payload with call attributes = %v", data))
		}

		if len(msg.ChannelName) > 0 && msg.Version == "v2" {
			data.AlertTitle(msg.ChannelName)
			data.AlertBody(emoji.Sprint(message))
			data.Custom("channel_name", msg.ChannelName)
		} else {
			data.Alert(emoji.Sprint(message))

			if len(msg.ChannelName) > 0 {
				data.Custom("channel_name", msg.ChannelName)
			}
		}
	} else {
		data.ContentAvailable()
	}

	incrementNotificationTotal(PUSH_NOTIFY_APPLE, pushType)
	data.Custom("type", pushType)





	if len(msg.AckId) > 0 {
		data.Custom("ack_id", msg.AckId)
	}

	if len(msg.ChannelId) > 0 {
		data.Custom("channel_id", msg.ChannelId)
		data.ThreadID(msg.ChannelId)
	}

	if len(msg.TeamId) > 0 {
		data.Custom("team_id", msg.TeamId)
	}

	if len(msg.SenderId) > 0 {
		data.Custom("sender_id", msg.SenderId)
	}

	if len(msg.SenderName) > 0 {
		data.Custom("sender_name", msg.SenderName)
	}

	if len(msg.PostId) > 0 {
		data.Custom("post_id", msg.PostId)
	}

	if len(msg.RootId) > 0 {
		data.Custom("root_id", msg.RootId)
	}

	if len(msg.OverrideUsername) > 0 {
		data.Custom("override_username", msg.OverrideUsername)
	}

	if len(msg.OverrideIconUrl) > 0 {
		data.Custom("override_icon_url", msg.OverrideIconUrl)
	}

	if len(msg.FromWebhook) > 0 {
		data.Custom("from_webhook", msg.FromWebhook)
	}

	if me.AppleClient != nil {
		LogInfo(fmt.Sprintf("Sending apple push notification for device=%v and type=%v", me.ApplePushSettings.Type, msg.Type))
		start := time.Now()
		res, err := me.AppleClient.Push(notification)
		observerNotificationResponse(PUSH_NOTIFY_APPLE, time.Since(start).Seconds())
		if err != nil {
			LogError(fmt.Sprintf("Failed to send apple push sid=%v did=%v err=%v type=%v", msg.ServerId, msg.DeviceId, err, me.ApplePushSettings.Type))
			incrementFailure(PUSH_NOTIFY_APPLE, pushType, "RequestError")
			return NewErrorPushResponse("unknown transport error")
		}

		if !res.Sent() {
			if res.Reason == apns.ReasonBadDeviceToken || res.Reason == apns.ReasonUnregistered || res.Reason == apns.ReasonMissingDeviceToken || res.Reason == apns.ReasonDeviceTokenNotForTopic {
				LogInfo(fmt.Sprintf("Failed to send apple push sending remove code res ApnsID=%v reason=%v code=%v type=%v", res.ApnsID, res.Reason, res.StatusCode, me.ApplePushSettings.Type))
				incrementRemoval(PUSH_NOTIFY_APPLE, pushType, res.Reason)
				return NewRemovePushResponse()
			}

			LogError(fmt.Sprintf("Failed to send apple push with res ApnsID=%v reason=%v code=%v type=%v", res.ApnsID, res.Reason, res.StatusCode, me.ApplePushSettings.Type))
			incrementFailure(PUSH_NOTIFY_APPLE, pushType, res.Reason)
			return NewErrorPushResponse("unknown send response error")
		}
	}

	if len(msg.AckId) > 0 {
		incrementSuccessWithAck(PUSH_NOTIFY_APPLE, pushType)
	} else {
		incrementSuccess(PUSH_NOTIFY_APPLE, pushType)
	}
	return NewOkPushResponse()
}
