package handler

import (
	"context"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"log/slog"
	"remnawave-tg-shop-bot/internal/config"
	"remnawave-tg-shop-bot/internal/cryptopay"
	"remnawave-tg-shop-bot/internal/database"
	"remnawave-tg-shop-bot/internal/payment"
	"remnawave-tg-shop-bot/internal/sync"
	"remnawave-tg-shop-bot/internal/translation"
	"remnawave-tg-shop-bot/internal/utils"
	"remnawave-tg-shop-bot/internal/yookasa"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	customerRepository *database.CustomerRepository
	purchaseRepository *database.PurchaseRepository
	cryptoPayClient    *cryptopay.Client
	yookasaClient      *yookasa.Client
	translation        *translation.Manager
	paymentService     *payment.PaymentService
	syncService        *sync.SyncService
	referralRepository *database.ReferralRepository
}

func NewHandler(
	syncService *sync.SyncService,
	paymentService *payment.PaymentService,
	translation *translation.Manager,
	customerRepository *database.CustomerRepository,
	purchaseRepository *database.PurchaseRepository,
	cryptoPayClient *cryptopay.Client,
	yookasaClient *yookasa.Client, referralRepository *database.ReferralRepository) *Handler {
	return &Handler{
		syncService:        syncService,
		paymentService:     paymentService,
		customerRepository: customerRepository,
		purchaseRepository: purchaseRepository,
		cryptoPayClient:    cryptoPayClient,
		yookasaClient:      yookasaClient,
		translation:        translation,
		referralRepository: referralRepository,
	}
}

const (
	CallbackBuy           = "buy"
	CallbackSell          = "sell"
	CallbackStart         = "start"
	CallbackConnect       = "connect"
	CallbackPayment       = "payment"
	CallbackTrial         = "trial"
	CallbackActivateTrial = "activate_trial"
	CallbackReferral      = "referral"
)

func (h Handler) ReferralCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	customer, _ := h.customerRepository.FindByTelegramId(ctx, update.CallbackQuery.From.ID)
	langCode := update.CallbackQuery.From.LanguageCode
	refCode := customer.TelegramID
	refLink := fmt.Sprintf("https://t.me/%s?start=ref_%d", update.CallbackQuery.Message.Message.From.Username, refCode)
	count, err := h.referralRepository.CountByReferrer(ctx, customer.TelegramID)
	if err != nil {
		slog.Error("error counting referrals", err)
	}
	text := fmt.Sprintf(h.translation.GetText(langCode, "referral_text"), refLink, count)
	callbackMessage := update.CallbackQuery.Message.Message
	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    callbackMessage.Chat.ID,
		MessageID: callbackMessage.ID,
		Text:      text,
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: h.translation.GetText(langCode, "back_button"), CallbackData: CallbackStart},
		}}},
	})
	if err != nil {
		slog.Error("Error sending referral message", err)
	}
}

func (h Handler) StartCommandHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	ctxWithTime, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	langCode := update.Message.From.LanguageCode
	existingCustomer, err := h.customerRepository.FindByTelegramId(ctx, update.Message.Chat.ID)
	if err != nil {
		slog.Error("error finding customer by telegram id", err)
	}

	if existingCustomer == nil {
		existingCustomer, err = h.customerRepository.Create(ctxWithTime, &database.Customer{
			TelegramID: update.Message.Chat.ID,
			Language:   langCode,
		})
		if err != nil {
			slog.Error("error creating customer", err)
			return
		}
		slog.Info("user created", "telegramId", update.Message.Chat.ID)

		if update.Message.Text != "" {
			arg := strings.Split(update.Message.Text, " ")[1]
			if strings.HasPrefix(arg, "ref_") {
				code := strings.TrimPrefix(arg, "ref_")
				referrerId, err := strconv.ParseInt(code, 10, 64)
				if err != nil {
					slog.Error("error parsing referrer id", err)
				}
				_, err = h.customerRepository.FindByTelegramId(ctx, referrerId)
				if err == nil {
					_, err := h.referralRepository.Create(ctx, referrerId, existingCustomer.TelegramID)
					if err != nil {
						slog.Error("error creating referral", err)
					}
					slog.Info("referral created", "referrerId", referrerId, "refereeId", existingCustomer.TelegramID)
				}
			}
		}

	} else {
		updates := map[string]interface{}{
			"language": langCode,
		}

		err = h.customerRepository.UpdateFields(ctx, existingCustomer.ID, updates)
		if err != nil {
			slog.Error("Error updating customer", err)
			return
		}
	}

	var inlineKeyboard [][]models.InlineKeyboardButton

	if existingCustomer.SubscriptionLink == nil {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "trial_button"), CallbackData: CallbackTrial},
		})
	}

	inlineKeyboard = append(inlineKeyboard, [][]models.InlineKeyboardButton{
		{{Text: h.translation.GetText(langCode, "buy_button"), CallbackData: "buy"}},
		{{Text: h.translation.GetText(langCode, "connect_button"), CallbackData: "connect"}},
		{{Text: h.translation.GetText(langCode, "referral_button"), CallbackData: "referral"}},
	}...)

	if config.ServerStatusURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "server_status_button"), URL: config.ServerStatusURL()},
		})
	}

	if config.SupportURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "support_button"), URL: config.SupportURL()},
		})
	}

	if config.FeedbackURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "feedback_button"), URL: config.FeedbackURL()},
		})
	}

	if config.ChannelURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "channel_button"), URL: config.ChannelURL()},
		})
	}

	if config.TosURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "tos_button"), URL: config.TosURL()},
		})
	}

	m, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   "🧹",
		ReplyMarkup: models.ReplyKeyboardRemove{
			RemoveKeyboard: true,
		},
	})

	if err != nil {
		slog.Error("Error sending removing reply keyboard", err)
	}

	_, err = b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: m.ID,
	})

	if err != nil {
		slog.Error("Error deleting message", err)
	}

	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		ParseMode: models.ParseModeMarkdown,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: inlineKeyboard,
		},
		Text: fmt.Sprintf(h.translation.GetText(langCode, "greeting"), bot.EscapeMarkdown(utils.BuildAvailableCountriesLists(langCode))),
	})
	if err != nil {
		slog.Error("Error sending /start message", err)
	}
}

func (h Handler) TrialCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	callback := update.CallbackQuery.Message.Message
	langCode := update.CallbackQuery.From.LanguageCode
	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    callback.Chat.ID,
		MessageID: callback.ID,
		Text:      h.translation.GetText(langCode, "trial_text"),
		ParseMode: models.ParseModeMarkdown,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: h.translation.GetText(langCode, "activate_trial_button"), CallbackData: CallbackActivateTrial}},
				{{Text: h.translation.GetText(langCode, "back_button"), CallbackData: CallbackStart}},
			},
		},
	})
	if err != nil {
		slog.Error("Error sending /trial message", err)
	}
}

func (h Handler) ActivateTrialCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	callback := update.CallbackQuery.Message.Message
	_, err := h.paymentService.ActivateTrial(ctx, update.CallbackQuery.From.ID)
	langCode := update.CallbackQuery.From.LanguageCode
	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    callback.Chat.ID,
		MessageID: callback.ID,
		Text:      h.translation.GetText(langCode, "trial_activated"),
		ParseMode: models.ParseModeMarkdown,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: h.translation.GetText(langCode, "connect_button"), CallbackData: "connect"}},
				{{Text: h.translation.GetText(langCode, "back_button"), CallbackData: CallbackStart}},
			},
		},
	})
	if err != nil {
		slog.Error("Error sending /trial message", err)
	}
}

func (h Handler) StartCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	ctxWithTime, cancel := context.WithTimeout(ctx, 5*time.Second)
	callback := update.CallbackQuery
	langCode := callback.From.LanguageCode

	defer cancel()
	existingCustomer, err := h.customerRepository.FindByTelegramId(ctx, callback.From.ID)
	if err != nil {
		slog.Error("error finding customer by telegram id", err)
	}

	if existingCustomer == nil {
		existingCustomer, err = h.customerRepository.Create(ctxWithTime, &database.Customer{
			TelegramID: update.Message.Chat.ID,
			Language:   langCode,
		})
		if err != nil {
			slog.Error("error creating customer", err)
			return
		}
		slog.Info("user created", "telegramId", update.Message.Chat.ID)
	} else {
		updates := map[string]interface{}{
			"language": langCode,
		}

		err = h.customerRepository.UpdateFields(ctx, existingCustomer.ID, updates)
		if err != nil {
			slog.Error("Error updating customer", err)
			return
		}
	}

	var inlineKeyboard [][]models.InlineKeyboardButton

	if existingCustomer.SubscriptionLink == nil {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "trial_button"), CallbackData: CallbackTrial},
		})
	}

	inlineKeyboard = append(inlineKeyboard, [][]models.InlineKeyboardButton{
		{{Text: h.translation.GetText(langCode, "buy_button"), CallbackData: "buy"}},
		{{Text: h.translation.GetText(langCode, "connect_button"), CallbackData: "connect"}},
		{{Text: h.translation.GetText(langCode, "referral_button"), CallbackData: "referral"}},
	}...)

	if config.ServerStatusURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "server_status_button"), URL: config.ServerStatusURL()},
		})
	}

	if config.SupportURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "support_button"), URL: config.SupportURL()},
		})
	}

	if config.FeedbackURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "feedback_button"), URL: config.FeedbackURL()},
		})
	}

	if config.ChannelURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "channel_button"), URL: config.ChannelURL()},
		})
	}

	if config.TosURL() != "" {
		inlineKeyboard = append(inlineKeyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "tos_button"), URL: config.TosURL()},
		})
	}

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: callback.Message.Message.Chat.ID,
		MessageID: callback.Message.Message.ID,
		ParseMode: models.ParseModeMarkdown,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: inlineKeyboard,
		},
		Text: fmt.Sprintf(h.translation.GetText(langCode, "greeting"), bot.EscapeMarkdown(utils.BuildAvailableCountriesLists(langCode))),
	})
	if err != nil {
		slog.Error("Error sending /start message", err)
	}
}

func (h Handler) BuyCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	callback := update.CallbackQuery.Message.Message
	langCode := update.CallbackQuery.From.LanguageCode

	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    callback.Chat.ID,
		MessageID: callback.ID,
		ParseMode: models.ParseModeMarkdown,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{Text: h.translation.GetText(langCode, "month_1"), CallbackData: fmt.Sprintf("%s?month=%d&amount=%d", CallbackSell, 1, config.Price1())},
					{Text: h.translation.GetText(langCode, "month_3"), CallbackData: fmt.Sprintf("%s?month=%d&amount=%d", CallbackSell, 3, config.Price3())},
					{Text: h.translation.GetText(langCode, "month_6"), CallbackData: fmt.Sprintf("%s?month=%d&amount=%d", CallbackSell, 6, config.Price6())},
					{Text: h.translation.GetText(langCode, "month_12"), CallbackData: fmt.Sprintf("%s?month=%d&amount=%d", CallbackSell, 12, config.Price12())},
				},
				{
					{Text: h.translation.GetText(langCode, "back_button"), CallbackData: CallbackStart},
				},
			},
		},
		Text: fmt.Sprintf(h.translation.GetText(langCode, "pricing_info"),
			config.Price1(),
			config.Price3(),
			config.Price6(),
			config.Price12()),
	})
	if err != nil {
		slog.Error("Error sending buy message", err)
	}
}

func (h Handler) SellCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	callback := update.CallbackQuery.Message.Message
	callbackQuery := parseCallbackData(update.CallbackQuery.Data)
	langCode := update.CallbackQuery.From.LanguageCode
	month := callbackQuery["month"]
	amount := callbackQuery["amount"]

	var keyboard [][]models.InlineKeyboardButton

	if config.IsCryptoPayEnabled() {
		keyboard = append(keyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "crypto_button"), CallbackData: fmt.Sprintf("%s?month=%s&invoiceType=%s&amount=%s", CallbackPayment, month, database.InvoiceTypeCrypto, amount)},
		})
	}

	if config.IsYookasaEnabled() {
		keyboard = append(keyboard, []models.InlineKeyboardButton{
			{Text: h.translation.GetText(langCode, "card_button"), CallbackData: fmt.Sprintf("%s?month=%s&invoiceType=%s&amount=%s", CallbackPayment, month, database.InvoiceTypeYookasa, amount)},
		})
	}

	if config.IsTelegramStarsEnabled() {
		keyboard = append(keyboard, []models.InlineKeyboardButton{
			{Text: "⭐Telegram Stars", CallbackData: fmt.Sprintf("%s?month=%s&invoiceType=%s&amount=%s", CallbackPayment, month, database.InvoiceTypeTelegram, amount)},
		})
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: h.translation.GetText(langCode, "back_button"), CallbackData: CallbackStart},
	})

	_, err := b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:    callback.Chat.ID,
		MessageID: callback.ID,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})

	if err != nil {
		slog.Error("Error sending sell message", err)
	}
}

func (h Handler) PaymentCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	callback := update.CallbackQuery.Message.Message
	callbackQuery := parseCallbackData(update.CallbackQuery.Data)
	month, err := strconv.Atoi(callbackQuery["month"])
	if err != nil {
		slog.Error("Error getting month from query", err)
		return
	}

	price, err := strconv.Atoi(callbackQuery["amount"])
	if err != nil {
		slog.Error("Error getting price from query", err)
		return
	}

	invoiceType := database.InvoiceType(callbackQuery["invoiceType"])

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	customer, err := h.customerRepository.FindByTelegramId(ctx, callback.Chat.ID)
	if err != nil {
		slog.Error("Error finding customer", err)
	}
	if customer == nil {
		slog.Error("customer not exist", "chatID", callback.Chat.ID, "error", err)
		return
	}

	paymentURL, err := h.paymentService.CreatePurchase(ctx, price, month, customer, invoiceType)

	if err != nil {
		slog.Error("Error creating payment", err)
	}

	langCode := update.CallbackQuery.From.LanguageCode

	_, err = b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:    callback.Chat.ID,
		MessageID: callback.ID,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{Text: h.translation.GetText(langCode, "pay_button"), URL: paymentURL},
					{Text: h.translation.GetText(langCode, "back_button"), CallbackData: fmt.Sprintf("%s?month=%d&amount=%d", CallbackSell, month, price)},
				},
			},
		},
	})
	if err != nil {
		slog.Error("Error updating sell message", err)
	}

}

func (h Handler) ConnectCommandHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	customer, err := h.customerRepository.FindByTelegramId(ctx, update.Message.Chat.ID)
	if err != nil {
		slog.Error("Error finding customer", err)
	}
	if customer == nil {
		slog.Error("customer not exist", "chatID", update.Message.Chat.ID, "error", err)
	}

	langCode := update.Message.From.LanguageCode

	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   buildConnectText(customer, langCode),
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: h.translation.GetText(langCode, "back_button"), CallbackData: CallbackStart}},
			},
		},
	})

	if err != nil {
		slog.Error("Error sending connect message", err)
	}
}

func (h Handler) ConnectCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	callback := update.CallbackQuery.Message.Message

	customer, err := h.customerRepository.FindByTelegramId(ctx, callback.Chat.ID)
	if err != nil {
		slog.Error("Error finding customer", err)
	}
	if customer == nil {
		slog.Error("customer not exist", "chatID", callback.Chat.ID, "error", err)
	}

	langCode := update.CallbackQuery.From.LanguageCode

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    callback.Chat.ID,
		MessageID: callback.ID,
		Text:      buildConnectText(customer, langCode),
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: h.translation.GetText(langCode, "back_button"), CallbackData: CallbackStart}},
			},
		},
	})

	if err != nil {
		slog.Error("Error sending connect message", err)
	}
}

func (h Handler) PreCheckoutCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	_, err := b.AnswerPreCheckoutQuery(ctx, &bot.AnswerPreCheckoutQueryParams{
		PreCheckoutQueryID: update.PreCheckoutQuery.ID,
		OK:                 true,
	})
	if err != nil {
		slog.Error("Error sending answer pre checkout query", err)
	}
}

func (h Handler) SuccessPaymentHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	purchaseId, err := strconv.Atoi(update.Message.SuccessfulPayment.InvoicePayload)
	if err != nil {
		slog.Error("Error parsing purchase id", err)
	}

	err = h.paymentService.ProcessPurchaseById(int64(purchaseId))
	if err != nil {
		slog.Error("Error processing purchase", err)
	}

}

func (h Handler) SyncUsersCommandHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.syncService.Sync()
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   "Users synced",
	})
	if err != nil {
		slog.Error("Error sending sync message", err)
	}
}

func buildConnectText(customer *database.Customer, langCode string) string {
	var info strings.Builder

	tm := translation.GetInstance()

	if customer.ExpireAt != nil {
		currentTime := time.Now()

		if currentTime.Before(*customer.ExpireAt) {
			formattedDate := customer.ExpireAt.Format("02.01.2006 15:04")

			subscriptionActiveText := tm.GetText(langCode, "subscription_active")
			info.WriteString(fmt.Sprintf(subscriptionActiveText, formattedDate))

			if customer.SubscriptionLink != nil && *customer.SubscriptionLink != "" {
				subscriptionLinkText := tm.GetText(langCode, "subscription_link")
				info.WriteString(fmt.Sprintf(subscriptionLinkText, *customer.SubscriptionLink))
			}
		} else {
			noSubscriptionText := tm.GetText(langCode, "no_subscription")
			info.WriteString(noSubscriptionText)
		}
	} else {
		noSubscriptionText := tm.GetText(langCode, "no_subscription")
		info.WriteString(noSubscriptionText)
	}

	return info.String()
}

func parseCallbackData(data string) map[string]string {
	result := make(map[string]string)

	parts := strings.Split(data, "?")
	if len(parts) < 2 {
		return result
	}

	params := strings.Split(parts[1], "&")
	for _, param := range params {
		kv := strings.SplitN(param, "=", 2)
		if len(kv) == 2 {
			result[kv[0]] = kv[1]
		}
	}

	return result
}
