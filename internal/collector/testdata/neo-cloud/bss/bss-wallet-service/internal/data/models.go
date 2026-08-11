package data

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	bssv1 "code.qianshi.cn/archer/neo-cloud/api/bss/v1"
	"code.qianshi.cn/archer/neo-cloud/bss/bss-wallet-service/internal/decimalfmt"
	"github.com/shopspring/decimal"
)

// MoneyMicros keeps the historical wallet API name while representing CNY as
// an exact decimal with 12 fractional digits.
type MoneyMicros struct {
	decimal.Decimal
}

const moneyMicrosScale = decimalfmt.Numeric30Scale12

func moneyMicros(value decimal.Decimal) MoneyMicros {
	return MoneyMicros{Decimal: value}
}

func (m MoneyMicros) Value() (driver.Value, error) {
	return formatMoneyMicros(m.Decimal)
}

func (m *MoneyMicros) Scan(value any) error {
	if m == nil {
		return fmt.Errorf("money micros scan target is nil")
	}
	switch v := value.(type) {
	case nil:
		*m = moneyMicros(decimal.Zero)
		return nil
	case []byte:
		parsed, err := parseMoneyMicros(string(v))
		if err != nil {
			return err
		}
		*m = moneyMicros(parsed)
		return nil
	case string:
		parsed, err := parseMoneyMicros(v)
		if err != nil {
			return err
		}
		*m = moneyMicros(parsed)
		return nil
	case int64:
		*m = moneyMicros(decimal.NewFromInt(v))
		return nil
	default:
		return fmt.Errorf("unsupported money value %T", value)
	}
}

type accountModel struct {
	ID                        int64              `gorm:"column:id;primaryKey"`
	OrgID                     string             `gorm:"column:org_id"`
	OrgType                   string             `gorm:"column:org_type"`
	Balance                   MoneyMicros        `gorm:"column:balance"`
	FrozenAmount              MoneyMicros        `gorm:"column:frozen_amount"`
	DebtLimit                 MoneyMicros        `gorm:"column:debt_limit"`
	CumulativeRechargeAmount  MoneyMicros        `gorm:"column:cumulative_recharge_amount"`
	CumulativeDeductionAmount MoneyMicros        `gorm:"column:cumulative_deduction_amount"`
	CumulativeRefundAmount    MoneyMicros        `gorm:"column:cumulative_refund_amount"`
	Currency                  string             `gorm:"column:currency"`
	Status                    bssv1.WalletStatus `gorm:"column:status"`
	AlertLevel                int                `gorm:"column:alert_level"`
	CreatedAt                 time.Time          `gorm:"column:created_at"`
	UpdatedAt                 time.Time          `gorm:"column:updated_at"`
}

func (accountModel) TableName() string { return "wallet.wallet_accounts" }

type transactionModel struct {
	ID              int64                 `gorm:"column:id;primaryKey"`
	TransactionID   string                `gorm:"column:transaction_id"`
	OrgID           string                `gorm:"column:org_id"`
	TxType          bssv1.TransactionType `gorm:"column:tx_type"`
	Amount          MoneyMicros           `gorm:"column:amount"`
	VoucherDeducted MoneyMicros           `gorm:"column:voucher_deducted"`
	BalanceDeducted MoneyMicros           `gorm:"column:balance_deducted"`
	BalanceBefore   MoneyMicros           `gorm:"column:balance_before"`
	BalanceAfter    MoneyMicros           `gorm:"column:balance_after"`
	Currency        string                `gorm:"column:currency"`
	ReferenceType   *string               `gorm:"column:reference_type"`
	ReferenceID     *string               `gorm:"column:reference_id"`
	RefID           *string               `gorm:"column:ref_id"`
	ActorID         *string               `gorm:"column:actor_id"`
	Description     *string               `gorm:"column:description"`
	IdempotencyKey  string                `gorm:"column:idempotency_key"`
	LineResults     []byte                `gorm:"column:line_results;type:jsonb"`
	CreatedAt       time.Time             `gorm:"column:created_at"`
	UpdatedAt       time.Time             `gorm:"column:updated_at"`
}

func (transactionModel) TableName() string { return "wallet.wallet_transactions" }

type deductionRequestIdemModel struct {
	ID                 int64     `gorm:"column:id;primaryKey"`
	OrgID              string    `gorm:"column:org_id"`
	IdempotencyKey     string    `gorm:"column:idempotency_key"`
	RefID              string    `gorm:"column:ref_id"`
	RequestFingerprint string    `gorm:"column:request_fingerprint"`
	TransactionID      string    `gorm:"column:transaction_id"`
	CreatedAt          time.Time `gorm:"column:created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at"`
}

func (deductionRequestIdemModel) TableName() string { return "wallet.deduction_request_idem" }

type deductionRefLatestModel struct {
	ID                 int64     `gorm:"column:id;primaryKey"`
	OrgID              string    `gorm:"column:org_id"`
	RefID              string    `gorm:"column:ref_id"`
	LastIdempotencyKey string    `gorm:"column:last_idempotency_key"`
	RequestFingerprint string    `gorm:"column:request_fingerprint"`
	TransactionID      string    `gorm:"column:transaction_id"`
	CreatedAt          time.Time `gorm:"column:created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at"`
}

func (deductionRefLatestModel) TableName() string { return "wallet.deduction_ref_latest" }

type paymentOrderModel struct {
	ID                    int64                    `gorm:"column:id;primaryKey"`
	OrderID               string                   `gorm:"column:order_id"`
	OrgID                 string                   `gorm:"column:org_id"`
	OrgType               string                   `gorm:"column:org_type"`
	RequestedAmount       MoneyMicros              `gorm:"column:requested_amount"`
	PaidAmount            MoneyMicros              `gorm:"column:paid_amount"`
	DebtOffsetAmount      MoneyMicros              `gorm:"column:debt_offset_amount"`
	RemainingAmount       MoneyMicros              `gorm:"column:remaining_amount"`
	FrozenRemainingAmount MoneyMicros              `gorm:"column:frozen_remaining_amount"`
	FeeAmount             MoneyMicros              `gorm:"column:fee_amount"`
	Currency              string                   `gorm:"column:currency"`
	Channel               bssv1.PaymentChannel     `gorm:"column:channel"`
	ChannelOrderID        *string                  `gorm:"column:channel_order_id"`
	ChannelTransactionID  *string                  `gorm:"column:channel_transaction_id"`
	CodeURL               *string                  `gorm:"column:code_url"`
	ClientToken           *string                  `gorm:"column:client_token"`
	CreatedBy             *string                  `gorm:"column:created_by"`
	ConfirmedBy           *string                  `gorm:"column:confirmed_by"`
	ConfirmNote           *string                  `gorm:"column:confirm_note"`
	Status                bssv1.PaymentOrderStatus `gorm:"column:status"`
	ExpiresAt             time.Time                `gorm:"column:expires_at"`
	PaidAt                *time.Time               `gorm:"column:paid_at"`
	CreatedAt             time.Time                `gorm:"column:created_at"`
	UpdatedAt             time.Time                `gorm:"column:updated_at"`
}

func (paymentOrderModel) TableName() string { return "wallet.payment_orders" }

type refundOrderModel struct {
	ID              int64                   `gorm:"column:id;primaryKey"`
	RefundID        string                  `gorm:"column:refund_id"`
	OrgID           string                  `gorm:"column:org_id"`
	OrgType         string                  `gorm:"column:org_type"`
	PaymentOrderID  string                  `gorm:"column:payment_order_id"`
	Amount          MoneyMicros             `gorm:"column:amount"`
	Currency        string                  `gorm:"column:currency"`
	Status          bssv1.RefundOrderStatus `gorm:"column:status"`
	Reason          *string                 `gorm:"column:reason"`
	ActorID         *string                 `gorm:"column:actor_id"`
	ActorType       *string                 `gorm:"column:actor_type"`
	RefundChannel   *bssv1.PaymentChannel   `gorm:"column:refund_channel"`
	ReviewerNote    *string                 `gorm:"column:reviewer_note"`
	ReviewerID      *string                 `gorm:"column:reviewer_id"`
	ChannelRefundID *string                 `gorm:"column:channel_refund_id"`
	FailureCode     *string                 `gorm:"column:failure_code"`
	FailureMessage  *string                 `gorm:"column:failure_message"`
	RetryCount      int                     `gorm:"column:retry_count"`
	NextRetryAt     *time.Time              `gorm:"column:next_retry_at"`
	LastRetryAt     *time.Time              `gorm:"column:last_retry_at"`
	ApprovedAt      *time.Time              `gorm:"column:approved_at"`
	CompletedAt     *time.Time              `gorm:"column:completed_at"`
	CreatedAt       time.Time               `gorm:"column:created_at"`
	UpdatedAt       time.Time               `gorm:"column:updated_at"`
}

func (refundOrderModel) TableName() string { return "wallet.refund_orders" }

type voucherModel struct {
	ID                 int64               `gorm:"column:id;primaryKey"`
	VoucherID          string              `gorm:"column:voucher_id"`
	OrgID              string              `gorm:"column:org_id"`
	Name               string              `gorm:"column:name"`
	TotalAmount        MoneyMicros         `gorm:"column:total_amount"`
	RemainingAmount    MoneyMicros         `gorm:"column:remaining_amount"`
	Currency           string              `gorm:"column:currency"`
	Priority           int32               `gorm:"column:priority"`
	AttributeFilters   []byte              `gorm:"column:attribute_filters;type:jsonb"`
	Source             bssv1.VoucherSource `gorm:"column:source"`
	Status             bssv1.VoucherStatus `gorm:"column:status"`
	IdempotencyKey     string              `gorm:"column:idempotency_key"`
	RequestFingerprint string              `gorm:"column:request_fingerprint"`
	Metadata           []byte              `gorm:"column:metadata;type:jsonb"`
	EffectiveAt        time.Time           `gorm:"column:effective_at"`
	ExpiresAt          time.Time           `gorm:"column:expires_at"`
	UsedAt             *time.Time          `gorm:"column:used_at"`
	ExpiredAt          *time.Time          `gorm:"column:expired_at"`
	RevokedAt          *time.Time          `gorm:"column:revoked_at"`
	CreatedAt          time.Time           `gorm:"column:created_at"`
	UpdatedAt          time.Time           `gorm:"column:updated_at"`
}

func (voucherModel) TableName() string { return "wallet.vouchers" }

type allocationModel struct {
	ID            int64       `gorm:"column:id;primaryKey"`
	AllocationID  string      `gorm:"column:allocation_id"`
	OrgID         string      `gorm:"column:org_id"`
	TransactionID string      `gorm:"column:transaction_id"`
	LineID        string      `gorm:"column:line_id"`
	VoucherID     string      `gorm:"column:voucher_id"`
	Amount        MoneyMicros `gorm:"column:amount"`
	CreatedAt     time.Time   `gorm:"column:created_at"`
}

func (allocationModel) TableName() string { return "wallet.allocations" }

type outboxEventModel struct {
	ID             int64        `gorm:"column:id;primaryKey"`
	EventID        string       `gorm:"column:event_id"`
	Topic          string       `gorm:"column:topic"`
	PartitionKey   string       `gorm:"column:partition_key"`
	OrgID          string       `gorm:"column:org_id"`
	EventType      string       `gorm:"column:event_type"`
	Payload        []byte       `gorm:"column:payload;type:jsonb"`
	Status         OutboxStatus `gorm:"column:status"`
	AttemptCount   int          `gorm:"column:attempt_count"`
	RetryCount     int          `gorm:"column:retry_count"`
	AvailableAt    *time.Time   `gorm:"column:available_at"`
	ClaimedAt      *time.Time   `gorm:"column:claimed_at"`
	ClaimedBy      *string      `gorm:"column:claimed_by"`
	ClaimToken     *string      `gorm:"column:claim_token"`
	LeaseExpiresAt *time.Time   `gorm:"column:lease_expires_at"`
	PublishedAt    *time.Time   `gorm:"column:published_at"`
	LastError      *string      `gorm:"column:last_error"`
	CreatedAt      time.Time    `gorm:"column:created_at"`
	UpdatedAt      time.Time    `gorm:"column:updated_at"`
}

func (outboxEventModel) TableName() string { return "wallet.wallet_outbox" }

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func paymentChannelPtrOrNil(value bssv1.PaymentChannel) *bssv1.PaymentChannel {
	if value == bssv1.PaymentChannel_PAYMENT_CHANNEL_UNSPECIFIED {
		return nil
	}
	return &value
}

func paymentChannelFromPtr(value *bssv1.PaymentChannel) bssv1.PaymentChannel {
	if value == nil {
		return bssv1.PaymentChannel_PAYMENT_CHANNEL_UNSPECIFIED
	}
	return *value
}

func parseMoneyMicros(raw string) (decimal.Decimal, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return decimal.Zero, fmt.Errorf("invalid money amount")
	}
	sign := decimal.NewFromInt(1)
	if strings.HasPrefix(raw, "-") {
		sign = decimal.NewFromInt(-1)
		raw = strings.TrimPrefix(raw, "-")
	} else if strings.HasPrefix(raw, "+") {
		raw = strings.TrimPrefix(raw, "+")
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 {
		return decimal.Zero, fmt.Errorf("invalid money amount %q", raw)
	}
	wholePart := parts[0]
	if wholePart == "" {
		wholePart = "0"
	}
	if !moneyDigitsOnly(wholePart) {
		return decimal.Zero, fmt.Errorf("invalid money whole amount")
	}
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	}
	if fracPart != "" && !moneyDigitsOnly(fracPart) {
		return decimal.Zero, fmt.Errorf("invalid money fractional amount")
	}
	if len(fracPart) > moneyMicrosScale {
		return decimal.Zero, fmt.Errorf("invalid money scale")
	}
	normalized := wholePart
	if len(parts) == 2 {
		normalized += "." + fracPart
	}
	value, err := decimal.NewFromString(normalized)
	if err != nil {
		return decimal.Zero, fmt.Errorf("invalid money amount: %w", err)
	}
	return checkedMoneyRange(value.Mul(sign))
}

func formatMoneyMicros(v decimal.Decimal) (string, error) {
	return decimalfmt.FormatNumeric30Scale12(v)
}

func moneyDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func checkedMoneyRange(value Money) (Money, error) {
	if decimalfmt.ExceedsNumeric30Scale12Range(value) {
		return decimal.Zero, ErrInvalidAmount
	}
	if decimalfmt.FractionalDigits(value) > moneyMicrosScale {
		return decimal.Zero, ErrInvalidAmount
	}
	return value, nil
}
