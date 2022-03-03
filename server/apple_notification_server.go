// Copyright (c) 2015 Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package server

import (
	"crypto/tls"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kyokomi/emoji"
	apns "github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sideshow/apns2/payload"
)

type AppleNotificationServer struct {
	ApplePushSettings  ApplePushSettings
	AppleClientManager *apns.ClientManager
	ApplePushCert      tls.Certificate
	AppleVoipCert      tls.Certificate
}

func NewAppleNotificationServer(settings ApplePushSettings) NotificationServer {
	return &AppleNotificationServer{ApplePushSettings: settings}
}

func (me *AppleNotificationServer) Initialize() bool {
	LogInfo(fmt.Sprintf("Initializing apple notification server for type=%v", me.ApplePushSettings.Type))

	me.AppleClientManager = apns.NewClientManager()
	me.AppleClientManager.MaxAge = 1000 * time.Hour

	var appleCertErr error
	if len(me.ApplePushSettings.ApplePushCertPrivate) > 0 {
		me.ApplePushCert, appleCertErr = certificate.FromPemFile(me.ApplePushSettings.ApplePushCertPrivate, me.ApplePushSettings.ApplePushCertPassword)
		if appleCertErr != nil {
			LogCritical(fmt.Sprintf("Failed to load the apple pem cert err=%v for type=%v", appleCertErr, me.ApplePushSettings.Type))
			return false
		}

		if me.ApplePushSettings.ApplePushUseDevelopment {
			me.AppleClientManager.Add(apns.NewClient(me.ApplePushCert).Development())
		} else {
			me.AppleClientManager.Add(apns.NewClient(me.ApplePushCert).Production())
		}

	} else {
		LogError(fmt.Sprintf("Apple push notifications not configured.  Missing ApplePushCertPrivate. for type=%v", me.ApplePushSettings.Type))
		return false
	}

	if len(me.ApplePushSettings.AppleVoipPushCertPrivate) > 0 {
		me.AppleVoipCert, appleCertErr = certificate.FromPemFile(me.ApplePushSettings.AppleVoipPushCertPrivate, me.ApplePushSettings.AppleVoipPushCertPassword)
		if appleCertErr != nil {
			LogCritical(fmt.Sprintf("Failed to load the apple pem cert err=%v for type=%v", appleCertErr, me.ApplePushSettings.Type))
			return false
		}

		if me.ApplePushSettings.ApplePushUseDevelopment {
			c := apns.NewClient(me.AppleVoipCert)
			c.Host = "https://api.development.push.apple.com"
			me.AppleClientManager.Add(c)
		} else {
			me.AppleClientManager.Add(apns.NewClient(me.AppleVoipCert).Production())
		}

		return true
	} else {
		LogError(fmt.Sprintf("Apple push notifications not configured.  Missing AppleVoipPushCertPrivate. for type=%v", me.ApplePushSettings.Type))
		return false
	}

}

func (me *AppleNotificationServer) SendNotification(msg *PushNotification) PushResponse {
	call_message := false
	if len(strings.Split(msg.Message, "springshot_call=")) > 1 {
		call_message = true
	}

	device_ids := strings.Split(msg.DeviceId, "|||")
	LogInfo(fmt.Sprintf("Springshot:: Msg DeviceIds OG = %v, Splits = %v", msg.DeviceId, device_ids))

	device_id_regular := msg.DeviceId
	var device_id_voip string

	call_kit := false
	if len(device_ids) > 1 {
		if len(device_ids[1]) > 0 {
			call_kit = true
		}
		device_id_regular = device_ids[0]
		device_id_voip = device_ids[1]
	}

	LogInfo(fmt.Sprintf("Springshot:: CallKit = %v, CallMessage = %v", call_kit, call_message))

	data := payload.NewPayload()
	data.Badge(msg.Badge)

	notification := &apns.Notification{}

	client_to_use := me.AppleClientManager.Get(me.ApplePushCert)
	if call_message && call_kit {
		notification.DeviceToken = device_id_voip
		notification.Topic = me.ApplePushSettings.AppleVoipPushTopic
		notification.Priority = apns.PriorityHigh
		notification.Expiration = time.Now().Add(time.Second * 60)
		notification.PushType = apns.EPushType(apns.PushTypeVOIP)

		client_to_use = me.AppleClientManager.Get(me.AppleVoipCert)
	} else {
		notification.DeviceToken = device_id_regular
		notification.Topic = me.ApplePushSettings.ApplePushTopic
	}

	notification.Payload = data

	var pushType = msg.Type
	if msg.Type != PUSH_TYPE_CLEAR {
		pushType = PUSH_TYPE_MESSAGE
		if call_message && call_kit {
			pushType = PUSH_TYPE_VOIP
		}
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
		if call_message { // This is a call message
			LogInfo(fmt.Sprintf("Springshot:: Call attributes = %v", message_split[1]))
			message = message_split[0]
			var call_attributes = strings.Split(message_split[1], "|")

			data.Sound("call_start.mp3")
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

	if client_to_use != nil {
		LogInfo(fmt.Sprintf("Sending apple push notification for device=%v and type=%v and notification=%v", me.ApplePushSettings.Type, pushType, notification))
		LogInfo(fmt.Sprintf("Notification - DeviceToken=%v and Topic=%v and Priority=%v and Expiration=%v and PushType=%v and Payload = %v", notification.DeviceToken, notification.Topic, notification.Priority, notification.Expiration, notification.PushType, notification.Payload))

		start := time.Now()

		res, err := client_to_use.Push(notification)

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
