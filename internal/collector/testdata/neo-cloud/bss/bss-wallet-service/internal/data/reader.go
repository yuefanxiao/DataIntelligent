package data

import (
	"context"
	"errors"
	"strings"
	"time"

	bssv1 "code.qianshi.cn/archer/neo-cloud/api/bss/v1"
	"gorm.io/gorm"
)

func ignoreRecordNotFound[T any](value *T, err error) (*T, error) {
	if err == nil {
		return value, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return nil, err
}

func recordNotFoundAs[T any](value *T, err error, notFound error) (*T, error) {
	if err == nil {
		return value, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, notFound
	}
	return nil, err
}

func (r *gormWalletRepo) GetAccount(ctx context.Context, orgID string) (*Account, error) {
	var row accountModel
	got, err := ignoreRecordNotFound(&row, r.db.WithContext(ctx).Where("org_id = ?", orgID).First(&row).Error)
	if got == nil || err != nil {
		return nil, err
	}
	return accountFromModel(*got), nil
}

func (r *gormWalletRepo) GetPaymentOrder(ctx context.Context, orderID string) (*PaymentOrderRecord, error) {
	var row paymentOrderModel
	got, err := recordNotFoundAs(&row, r.db.WithContext(ctx).Where("order_id = ?", orderID).First(&row).Error, ErrPaymentOrderNotFound)
	if got == nil || err != nil {
		return nil, err
	}
	return paymentOrderRecordFromModel(*got), nil
}

func (r *gormWalletRepo) GetPaymentOrderByChannelOrder(ctx context.Context, channelOrderID string) (*PaymentOrderRecord, error) {
	var row paymentOrderModel
	got, err := recordNotFoundAs(&row, r.db.WithContext(ctx).Where("channel_order_id = ?", channelOrderID).First(&row).Error, ErrPaymentOrderNotFound)
	if got == nil || err != nil {
		return nil, err
	}
	return paymentOrderRecordFromModel(*got), nil
}

func (r *gormWalletRepo) GetRefundOrder(ctx context.Context, refundID string) (*RefundOrderRecord, error) {
	var row refundOrderModel
	got, err := recordNotFoundAs(&row, r.db.WithContext(ctx).Where("refund_id = ?", refundID).First(&row).Error, ErrRefundOrderNotFound)
	if got == nil || err != nil {
		return nil, err
	}
	return refundOrderRecordFromModel(*got), nil
}

func (r *gormWalletRepo) GetRefundOrderByChannelRefund(ctx context.Context, channelRefundID string) (*RefundOrderRecord, error) {
	var row refundOrderModel
	got, err := recordNotFoundAs(&row, r.db.WithContext(ctx).Where("channel_refund_id = ?", channelRefundID).First(&row).Error, ErrRefundOrderNotFound)
	if got == nil || err != nil {
		return nil, err
	}
	return refundOrderRecordFromModel(*got), nil
}

func (r *gormWalletRepo) GetTransaction(ctx context.Context, transactionID string) (*TransactionRecord, error) {
	var row transactionModel
	got, err := recordNotFoundAs(&row, r.db.WithContext(ctx).Where("transaction_id = ?", transactionID).First(&row).Error, ErrTransactionNotFound)
	if got == nil || err != nil {
		return nil, err
	}
	return transactionRecordFromModel(*got), nil
}

func (r *gormWalletRepo) ListTransactions(ctx context.Context, filter ListTransactionsFilter) (*CursorPage[TransactionRecord], error) {
	cursor := normalizeCursorParams(filter.CursorParams)
	var rows []transactionModel
	base := func() *gorm.DB {
		query := r.db.WithContext(ctx).Model(&transactionModel{})
		if filter.OrgID != "" {
			query = query.Where("org_id = ?", filter.OrgID)
		}
		if filter.Type != bssv1.TransactionType_TRANSACTION_TYPE_UNSPECIFIED {
			query = query.Where("tx_type = ?", filter.Type)
		}
		return query
	}
	query, err := applyCreatedIDCursor(base, "transaction_id", cursor)
	if err != nil {
		return nil, err
	}
	if err := query.Order(cursorOrderExpr(cursor, "created_at", "id")).Limit(int(cursor.Limit) + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]TransactionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, *transactionRecordFromModel(row))
	}
	return cursorPageFromItems(records, cursor.Limit, func(item TransactionRecord) string { return item.TransactionID }), nil
}

func (r *gormWalletRepo) ListPaymentOrders(ctx context.Context, filter ListPaymentOrdersFilter) (*CursorPage[PaymentOrderRecord], error) {
	cursor := normalizeCursorParams(filter.CursorParams)
	var rows []paymentOrderModel
	base := func() *gorm.DB {
		query := r.db.WithContext(ctx).Model(&paymentOrderModel{})
		if filter.OrgID != "" {
			query = query.Where("org_id = ?", filter.OrgID)
		}
		if filter.Status != bssv1.PaymentOrderStatus_PAYMENT_ORDER_STATUS_UNSPECIFIED {
			query = query.Where("status = ?", filter.Status)
		}
		return query
	}
	query, err := applyCreatedIDCursor(base, "order_id", cursor)
	if err != nil {
		return nil, err
	}
	if err := query.Order(cursorOrderExpr(cursor, "created_at", "id")).Limit(int(cursor.Limit) + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]PaymentOrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, *paymentOrderRecordFromModel(row))
	}
	return cursorPageFromItems(records, cursor.Limit, func(item PaymentOrderRecord) string { return item.OrderID }), nil
}

func (r *gormWalletRepo) ListRefundOrders(ctx context.Context, filter ListRefundOrdersFilter) (*CursorPage[RefundOrderRecord], error) {
	cursor := normalizeCursorParams(filter.CursorParams)
	var rows []refundOrderModel
	base := func() *gorm.DB {
		query := r.db.WithContext(ctx).Model(&refundOrderModel{})
		if filter.OrgID != "" {
			query = query.Where("org_id = ?", filter.OrgID)
		}
		if filter.Status != bssv1.RefundOrderStatus_REFUND_ORDER_STATUS_UNSPECIFIED {
			query = query.Where("status = ?", filter.Status)
		}
		return query
	}
	query, err := applyCreatedIDCursor(base, "refund_id", cursor)
	if err != nil {
		return nil, err
	}
	if err := query.Order(cursorOrderExpr(cursor, "created_at", "id")).Limit(int(cursor.Limit) + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]RefundOrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, *refundOrderRecordFromModel(row))
	}
	return cursorPageFromItems(records, cursor.Limit, func(item RefundOrderRecord) string { return item.RefundID }), nil
}

func (r *gormWalletRepo) ListBSSRechargeOrders(ctx context.Context, filter ListBSSRechargeOrdersFilter) (*CursorPage[BSSRechargeOrderRecord], error) {
	cursor := normalizeCursorParams(filter.CursorParams)
	base := func() *gorm.DB {
		return applyBSSRechargeOrderFilters(r.bssRechargeOrderQuery(ctx), filter)
	}
	query, err := applyAliasedCreatedIDCursor(base, "payments.order_id", "payments.created_at", "payments.id", cursor)
	if err != nil {
		return nil, err
	}

	var rows []bssRechargeOrderRow
	err = query.Select(`
payments.order_id,
payments.org_id,
payments.org_type,
payments.requested_amount,
payments.paid_amount,
payments.debt_offset_amount,
payments.currency,
payments.status AS payment_status,
payments.channel,
payments.channel_order_id,
payments.channel_transaction_id,
payments.confirm_note AS remark,
txs.transaction_id AS wallet_transaction_id,
payments.paid_at,
payments.created_at,
CASE WHEN payments.remaining_amount > payments.frozen_remaining_amount THEN payments.remaining_amount - payments.frozen_remaining_amount ELSE 0 END AS refundable_amount,
txs.created_at AS credited_at`).
		Order(cursorOrderExpr(cursor, "payments.created_at", "payments.id")).
		Limit(int(cursor.Limit) + 1).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	records := make([]BSSRechargeOrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, bssRechargeOrderRecordFromRow(row))
	}
	return cursorPageFromItems(records, cursor.Limit, func(item BSSRechargeOrderRecord) string { return item.OrderID }), nil
}

func (r *gormWalletRepo) GetBSSRechargeOrder(ctx context.Context, orderID string) (*BSSRechargeOrderRecord, error) {
	filter := ListBSSRechargeOrdersFilter{CursorParams: CursorParams{Limit: 2}}
	query := applyBSSRechargeOrderFilters(r.bssRechargeOrderQuery(ctx), filter).Where("payments.order_id = ?", orderID)

	var row bssRechargeOrderRow
	err := query.Select(`
payments.order_id,
payments.org_id,
payments.org_type,
payments.requested_amount,
payments.paid_amount,
payments.debt_offset_amount,
payments.currency,
payments.status AS payment_status,
payments.channel,
payments.channel_order_id,
payments.channel_transaction_id,
payments.confirm_note AS remark,
txs.transaction_id AS wallet_transaction_id,
payments.paid_at,
payments.created_at,
CASE WHEN payments.remaining_amount > payments.frozen_remaining_amount THEN payments.remaining_amount - payments.frozen_remaining_amount ELSE 0 END AS refundable_amount,
txs.created_at AS credited_at`).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPaymentOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	rec := bssRechargeOrderRecordFromRow(row)
	return &rec, nil
}

func (r *gormWalletRepo) ListBSSRefundOrders(ctx context.Context, filter ListBSSRefundOrdersFilter) (*CursorPage[BSSRefundOrderRecord], error) {
	cursor := normalizeCursorParams(filter.CursorParams)
	base := func() *gorm.DB {
		return applyBSSRefundOrderFilters(r.bssRefundOrderQuery(ctx), filter)
	}
	query, err := applyAliasedCreatedIDCursor(base, "refunds.refund_id", "refunds.created_at", "refunds.id", cursor)
	if err != nil {
		return nil, err
	}

	var rows []bssRefundOrderRow
	err = query.Select(`
refunds.refund_id,
refunds.org_id,
refunds.org_type,
refunds.payment_order_id,
refunds.amount,
refunds.currency,
refunds.status,
refunds.reason,
refunds.actor_id,
refunds.actor_type,
refunds.refund_channel,
refunds.channel_refund_id,
payments.channel_transaction_id,
txs.transaction_id AS wallet_transaction_id,
refunds.approved_at,
refunds.completed_at,
refunds.created_at,
refunds.updated_at,
refunds.reviewer_id,
refunds.reviewer_note`).
		Order(cursorOrderExpr(cursor, "refunds.created_at", "refunds.id")).
		Limit(int(cursor.Limit) + 1).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	records := make([]BSSRefundOrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, bssRefundOrderRecordFromRow(row))
	}
	return cursorPageFromItems(records, cursor.Limit, func(item BSSRefundOrderRecord) string { return item.RefundID }), nil
}

func (r *gormWalletRepo) GetBSSRefundOrder(ctx context.Context, refundID string) (*BSSRefundOrderRecord, error) {
	query := applyBSSRefundOrderFilters(r.bssRefundOrderQuery(ctx), ListBSSRefundOrdersFilter{}).Where("refunds.refund_id = ?", refundID)
	var row bssRefundOrderRow
	err := query.Select(`
refunds.refund_id,
refunds.org_id,
refunds.org_type,
refunds.payment_order_id,
refunds.amount,
refunds.currency,
refunds.status,
refunds.reason,
refunds.actor_id,
refunds.actor_type,
refunds.refund_channel,
refunds.channel_refund_id,
payments.channel_transaction_id,
txs.transaction_id AS wallet_transaction_id,
refunds.approved_at,
refunds.completed_at,
refunds.created_at,
refunds.updated_at,
refunds.reviewer_id,
refunds.reviewer_note`).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRefundOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	rec := bssRefundOrderRecordFromRow(row)
	return &rec, nil
}

func (r *gormWalletRepo) BatchGetWalletAmountSummaries(ctx context.Context, orgIDs []string, startTime, endTime time.Time) ([]WalletAmountSummaryRecord, error) {
	ids := uniqueNonEmptyStrings(orgIDs)
	if len(ids) == 0 {
		return nil, nil
	}

	type amountRow struct {
		OrgID    string      `gorm:"column:org_id"`
		Amount   MoneyMicros `gorm:"column:amount"`
		Currency string      `gorm:"column:currency"`
	}

	byOrg := make(map[string]*WalletAmountSummaryRecord, len(ids))
	ensure := func(orgID, currency string) *WalletAmountSummaryRecord {
		row := byOrg[orgID]
		if row == nil {
			row = &WalletAmountSummaryRecord{OrgID: orgID, Currency: currency}
			byOrg[orgID] = row
		}
		if row.Currency == "" {
			row.Currency = currency
		}
		return row
	}

	rechargeQuery := r.db.WithContext(ctx).
		Table("wallet.payment_orders").
		Select("org_id, CAST(COALESCE(SUM(paid_amount), 0) AS TEXT) AS amount, MAX(currency) AS currency").
		Where("org_id IN ?", ids).
		Where("paid_at IS NOT NULL").
		Where("status IN ?", []bssv1.PaymentOrderStatus{
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_PAID,
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_PARTIALLY_REFUNDED,
		})
	if !startTime.IsZero() {
		rechargeQuery = rechargeQuery.Where("paid_at >= ?", startTime.UTC())
	}
	if !endTime.IsZero() {
		rechargeQuery = rechargeQuery.Where("paid_at < ?", endTime.UTC())
	}
	var rechargeRows []amountRow
	if err := rechargeQuery.Group("org_id").Scan(&rechargeRows).Error; err != nil {
		return nil, err
	}
	for _, row := range rechargeRows {
		ensure(row.OrgID, row.Currency).RechargeAmount = row.Amount.Decimal
	}

	refundQuery := r.db.WithContext(ctx).
		Table("wallet.refund_orders").
		Select("org_id, CAST(COALESCE(SUM(amount), 0) AS TEXT) AS amount, MAX(currency) AS currency").
		Where("org_id IN ?", ids).
		Where("completed_at IS NOT NULL").
		Where("status = ?", bssv1.RefundOrderStatus_REFUND_SUCCEEDED)
	if !startTime.IsZero() {
		refundQuery = refundQuery.Where("completed_at >= ?", startTime.UTC())
	}
	if !endTime.IsZero() {
		refundQuery = refundQuery.Where("completed_at < ?", endTime.UTC())
	}
	var refundRows []amountRow
	if err := refundQuery.Group("org_id").Scan(&refundRows).Error; err != nil {
		return nil, err
	}
	for _, row := range refundRows {
		ensure(row.OrgID, row.Currency).RefundAmount = row.Amount.Decimal
	}

	out := make([]WalletAmountSummaryRecord, 0, len(byOrg))
	for _, orgID := range ids {
		if row := byOrg[orgID]; row != nil {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (r *gormWalletRepo) GetWalletOrgOperationSummary(ctx context.Context, orgID string) (WalletOrgOperationSummaryRecord, error) {
	var pendingPaymentCount int64
	if err := r.db.WithContext(ctx).
		Model(&paymentOrderModel{}).
		Where("org_id = ? AND status = ?", orgID, bssv1.PaymentOrderStatus_PAYMENT_ORDER_PENDING).
		Count(&pendingPaymentCount).Error; err != nil {
		return WalletOrgOperationSummaryRecord{}, err
	}

	var pendingRefundCount int64
	if err := r.db.WithContext(ctx).
		Model(&refundOrderModel{}).
		Where("org_id = ? AND status IN ?", orgID, []bssv1.RefundOrderStatus{
			bssv1.RefundOrderStatus_REFUND_PENDING_APPROVAL,
			bssv1.RefundOrderStatus_REFUND_APPROVED,
			bssv1.RefundOrderStatus_REFUND_PROCESSING,
			bssv1.RefundOrderStatus_REFUND_PENDING,
		}).
		Count(&pendingRefundCount).Error; err != nil {
		return WalletOrgOperationSummaryRecord{}, err
	}

	return WalletOrgOperationSummaryRecord{
		PendingPaymentCount: pendingPaymentCount,
		PendingRefundCount:  pendingRefundCount,
	}, nil
}

func (r *gormWalletRepo) ListVouchers(ctx context.Context, filter ListVouchersFilter) (*CursorPage[VoucherRecord], error) {
	cursor := normalizeCursorParams(filter.CursorParams)
	var rows []voucherModel
	base := func() *gorm.DB {
		query := r.db.WithContext(ctx).Model(&voucherModel{})
		if filter.OrgID != "" {
			query = query.Where("org_id = ?", filter.OrgID)
		}
		if filter.Status != bssv1.VoucherStatus_VOUCHER_STATUS_UNSPECIFIED {
			query = query.Where("status = ?", filter.Status)
		}
		return query
	}
	query, err := applyCreatedIDCursor(base, "voucher_id", cursor)
	if err != nil {
		return nil, err
	}
	if err := query.Order(cursorOrderExpr(cursor, "created_at", "id")).Limit(int(cursor.Limit) + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]VoucherRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, *voucherRecordFromModel(row))
	}
	return cursorPageFromItems(records, cursor.Limit, func(item VoucherRecord) string { return item.VoucherID }), nil
}

func (r *gormWalletRepo) GetVoucherByIdempotencyKey(ctx context.Context, idempotencyKey string) (*VoucherRecord, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, nil
	}
	var row voucherModel
	got, err := ignoreRecordNotFound(&row, r.db.WithContext(ctx).Where("idempotency_key = ?", idempotencyKey).First(&row).Error)
	if got == nil || err != nil {
		return nil, err
	}
	return voucherRecordFromModel(*got), nil
}

func (r *gormWalletRepo) BatchGetVouchers(ctx context.Context, voucherIDs []string) ([]*VoucherRecord, error) {
	ids := uniqueNonEmptyStrings(voucherIDs)
	if len(ids) == 0 {
		return []*VoucherRecord{}, nil
	}
	var rows []voucherModel
	if err := r.db.WithContext(ctx).Where("voucher_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]*VoucherRecord, len(rows))
	for _, row := range rows {
		rec := voucherRecordFromModel(row)
		byID[rec.VoucherID] = rec
	}
	return orderedVoucherRecords(voucherIDs, byID), nil
}

func (r *gormWalletRepo) BatchGetVouchersByIdempotencyKeys(ctx context.Context, idempotencyKeys []string) ([]*VoucherRecord, error) {
	keys := uniqueNonEmptyStrings(idempotencyKeys)
	if len(keys) == 0 {
		return []*VoucherRecord{}, nil
	}
	var rows []voucherModel
	if err := r.db.WithContext(ctx).Where("idempotency_key IN ?", keys).Find(&rows).Error; err != nil {
		return nil, err
	}
	byKey := make(map[string]*VoucherRecord, len(rows))
	for _, row := range rows {
		rec := voucherRecordFromModel(row)
		byKey[rec.IdempotencyKey] = rec
	}
	out := make([]*VoucherRecord, 0, len(rows))
	seen := make(map[string]struct{}, len(idempotencyKeys))
	for _, key := range idempotencyKeys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if rec := byKey[key]; rec != nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (r *gormWalletRepo) BatchGetVoucherUsageSummaries(ctx context.Context, voucherIDs []string) ([]VoucherUsageSummaryRecord, error) {
	ids := uniqueNonEmptyStrings(voucherIDs)
	if len(ids) == 0 {
		return []VoucherUsageSummaryRecord{}, nil
	}
	var vouchers []voucherModel
	if err := r.db.WithContext(ctx).Where("voucher_id IN ?", ids).Find(&vouchers).Error; err != nil {
		return nil, err
	}
	type allocationUsage struct {
		VoucherID  string
		LastUsedAt *time.Time
	}
	var usages []allocationUsage
	if err := r.db.WithContext(ctx).Model(&allocationModel{}).
		Select("voucher_id, MAX(created_at) AS last_used_at").
		Where("voucher_id IN ?", ids).
		Group("voucher_id").
		Scan(&usages).Error; err != nil {
		return nil, err
	}
	lastUsedByVoucher := make(map[string]*time.Time, len(usages))
	for _, usage := range usages {
		lastUsedByVoucher[usage.VoucherID] = usage.LastUsedAt
	}
	byID := make(map[string]VoucherUsageSummaryRecord, len(vouchers))
	for _, voucher := range vouchers {
		rec := voucherRecordFromModel(voucher)
		used, err := checkedSubMoney(rec.TotalAmountMicros, rec.RemainingMicros)
		if err != nil {
			return nil, err
		}
		byID[rec.VoucherID] = VoucherUsageSummaryRecord{
			VoucherID:       rec.VoucherID,
			OrgID:           rec.OrgID,
			TotalAmount:     rec.TotalAmountMicros,
			RemainingAmount: rec.RemainingMicros,
			UsedAmount:      used,
			Status:          rec.Status,
			LastUsedAt:      lastUsedByVoucher[rec.VoucherID],
		}
	}
	out := make([]VoucherUsageSummaryRecord, 0, len(byID))
	seen := make(map[string]struct{}, len(voucherIDs))
	for _, id := range voucherIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if rec, ok := byID[id]; ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (r *gormWalletRepo) ListVoucherLedgerEntries(ctx context.Context, voucherID string, cursor CursorParams) (*CursorPage[VoucherLedgerEntryRecord], error) {
	cursor = normalizeCursorParams(cursor)
	var rows []allocationModel
	base := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&allocationModel{}).Where("voucher_id = ?", voucherID)
	}
	query, err := applyCreatedIDCursor(base, "allocation_id", cursor)
	if err != nil {
		return nil, err
	}
	if err := query.Order(cursorOrderExpr(cursor, "created_at", "id")).Limit(int(cursor.Limit) + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]VoucherLedgerEntryRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, VoucherLedgerEntryRecord{
			LedgerEntryID: row.AllocationID,
			VoucherID:     row.VoucherID,
			OrgID:         row.OrgID,
			TransactionID: row.TransactionID,
			LineID:        row.LineID,
			Amount:        row.Amount.Decimal,
			CreatedAt:     row.CreatedAt,
		})
	}
	return cursorPageFromItems(records, cursor.Limit, func(item VoucherLedgerEntryRecord) string { return item.LedgerEntryID }), nil
}

func (r *gormWalletRepo) ListEligibleVouchers(ctx context.Context, orgID string, now time.Time, attrs map[string][]string) ([]*VoucherRecord, error) {
	var rows []voucherModel
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND status = ? AND effective_at <= ? AND expires_at > ? AND remaining_amount > ?",
			orgID,
			bssv1.VoucherStatus_VOUCHER_ACTIVE,
			now,
			now,
			moneyMicros(Money{}),
		).
		Order("priority ASC, expires_at ASC, created_at ASC, id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*VoucherRecord, 0, len(rows))
	for _, row := range rows {
		rec := voucherRecordFromModel(row)
		if !attributeFiltersMatch(rec.AttributeFilters, attrs) {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func (r *gormWalletRepo) ListFIFOPaymentOrders(ctx context.Context, orgID string) ([]*PaymentOrderRecord, error) {
	var rows []paymentOrderModel
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND status IN ? AND remaining_amount > frozen_remaining_amount",
			orgID,
			[]bssv1.PaymentOrderStatus{
				bssv1.PaymentOrderStatus_PAYMENT_ORDER_PAID,
				bssv1.PaymentOrderStatus_PAYMENT_ORDER_PARTIALLY_REFUNDED,
			},
		).
		Order("paid_at IS NULL ASC, paid_at ASC, created_at ASC, id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*PaymentOrderRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, paymentOrderRecordFromModel(row))
	}
	return out, nil
}

func (r *gormWalletRepo) ListPendingPaymentOrders(ctx context.Context) ([]PendingPaymentSummary, error) {
	var rows []paymentOrderModel
	err := r.db.WithContext(ctx).
		Where("status IN ?", []bssv1.PaymentOrderStatus{
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_PENDING,
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_EXPIRED,
		}).
		Order("created_at ASC, id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]PendingPaymentSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, PendingPaymentSummary{
			OrderID:        row.OrderID,
			OrgID:          row.OrgID,
			Channel:        row.Channel,
			ChannelOrderID: stringFromPtr(row.ChannelOrderID),
			Status:         row.Status,
			AmountMicros:   row.RequestedAmount.Decimal,
			ExpiresAt:      row.ExpiresAt,
		})
	}
	return out, nil
}

func (r *gormWalletRepo) ListPendingRefundOrders(ctx context.Context, now time.Time) ([]PendingRefundSummary, error) {
	type pendingRefundRow struct {
		RefundID                          string
		OrgID                             string
		PaymentOrderID                    string
		Channel                           bssv1.PaymentChannel
		ChannelRefundID                   *string
		Status                            bssv1.RefundOrderStatus
		RetryCount                        int
		NextRetryAt                       *time.Time
		SourcePaymentChannelTransactionID *string
		SourcePaymentTotalAmount          MoneyMicros
		Amount                            MoneyMicros
	}

	var rows []pendingRefundRow
	refundTable := refundOrderModel{}.TableName()
	paymentTable := paymentOrderModel{}.TableName()
	err := r.db.WithContext(ctx).
		Table(refundTable+" AS refunds").
		Select(`
			refunds.refund_id,
			refunds.org_id,
			refunds.payment_order_id,
			refunds.channel_refund_id,
			refunds.status,
			refunds.retry_count,
			refunds.next_retry_at,
			refunds.amount,
			payments.channel,
			payments.channel_transaction_id AS source_payment_channel_transaction_id,
			payments.requested_amount AS source_payment_total_amount`).
		Joins("JOIN "+paymentTable+" AS payments ON payments.order_id = refunds.payment_order_id").
		Where("refunds.status IN ?", []bssv1.RefundOrderStatus{
			bssv1.RefundOrderStatus_REFUND_PENDING,
			bssv1.RefundOrderStatus_REFUND_PROCESSING,
		}).
		Where("(refunds.status <> ? OR refunds.next_retry_at IS NULL OR refunds.next_retry_at <= ?)",
			bssv1.RefundOrderStatus_REFUND_PENDING,
			now,
		).
		Order("refunds.created_at ASC, refunds.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]PendingRefundSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, PendingRefundSummary{
			RefundID:                          row.RefundID,
			OrgID:                             row.OrgID,
			PaymentOrderID:                    row.PaymentOrderID,
			Channel:                           row.Channel,
			ChannelRefundID:                   stringFromPtr(row.ChannelRefundID),
			Status:                            row.Status,
			RetryCount:                        row.RetryCount,
			NextRetryAt:                       row.NextRetryAt,
			SourcePaymentChannelTransactionID: stringFromPtr(row.SourcePaymentChannelTransactionID),
			SourcePaymentTotalAmountMicros:    row.SourcePaymentTotalAmount.Decimal,
			AmountMicros:                      row.Amount.Decimal,
		})
	}
	return out, nil
}

func (r *gormWalletRepo) GeneralVoucherBalance(ctx context.Context, orgID string, now time.Time) (Money, error) {
	var totalRaw string
	err := r.db.WithContext(ctx).Model(&voucherModel{}).
		Select(generalVoucherBalanceSelect(r.db)).
		Where(
			"org_id = ? AND status = ? AND effective_at <= ? AND expires_at > ?",
			orgID,
			bssv1.VoucherStatus_VOUCHER_ACTIVE,
			now,
			now,
		).
		Where(generalVoucherScopeCondition(r.db)).
		Scan(&totalRaw).Error
	if err != nil {
		return Money{}, err
	}
	return parseMoneyMicros(totalRaw)
}

func (r *gormWalletRepo) GetDeductionByIdempotencyKey(ctx context.Context, orgID, idemKey string) (*DeductionRequestIdemRecord, error) {
	var row deductionRequestIdemModel
	got, err := ignoreRecordNotFound(&row, r.db.WithContext(ctx).Where("org_id = ? AND idempotency_key = ?", orgID, idemKey).First(&row).Error)
	if got == nil || err != nil {
		return nil, err
	}
	return deductionRequestIdemRecordFromModel(*got), nil
}

func (r *gormWalletRepo) GetDeductionByRef(ctx context.Context, orgID, refID string) (*DeductionRefLatestRecord, error) {
	var row deductionRefLatestModel
	got, err := ignoreRecordNotFound(&row, r.db.WithContext(ctx).Where("org_id = ? AND ref_id = ?", orgID, refID).First(&row).Error)
	if got == nil || err != nil {
		return nil, err
	}
	return deductionRefLatestRecordFromModel(*got), nil
}

func (r *gormWalletRepo) CheckTransactionIdempotencyKey(ctx context.Context, orgID, idemKey string) (bool, error) {
	if idemKey == "" {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&transactionModel{}).
		Where("org_id = ? AND idempotency_key = ?", orgID, idemKey).
		Count(&count).Error
	return count > 0, err
}

func (r *gormWalletRepo) CheckClientToken(ctx context.Context, orgID, clientToken string) (string, error) {
	if clientToken == "" {
		return "", nil
	}
	var row paymentOrderModel
	got, err := ignoreRecordNotFound(&row, r.db.WithContext(ctx).
		Select("order_id").
		Where("org_id = ? AND client_token = ?", orgID, clientToken).
		First(&row).Error)
	if got == nil || err != nil {
		return "", err
	}
	return got.OrderID, nil
}

func deductionRequestIdemRecordFromModel(item deductionRequestIdemModel) *DeductionRequestIdemRecord {
	return &DeductionRequestIdemRecord{
		ID:                 item.ID,
		OrgID:              item.OrgID,
		IdempotencyKey:     item.IdempotencyKey,
		RefID:              item.RefID,
		RequestFingerprint: item.RequestFingerprint,
		TransactionID:      item.TransactionID,
		CreatedAt:          item.CreatedAt,
		UpdatedAt:          item.UpdatedAt,
	}
}

func deductionRefLatestRecordFromModel(item deductionRefLatestModel) *DeductionRefLatestRecord {
	return &DeductionRefLatestRecord{
		ID:                 item.ID,
		OrgID:              item.OrgID,
		RefID:              item.RefID,
		LastIdempotencyKey: item.LastIdempotencyKey,
		RequestFingerprint: item.RequestFingerprint,
		TransactionID:      item.TransactionID,
		CreatedAt:          item.CreatedAt,
		UpdatedAt:          item.UpdatedAt,
	}
}

func deductionRequestIdemModelFromRecord(item DeductionRequestIdemRecord) deductionRequestIdemModel {
	return deductionRequestIdemModel(item)
}

func uniqueNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func orderedVoucherRecords(ids []string, byID map[string]*VoucherRecord) []*VoucherRecord {
	out := make([]*VoucherRecord, 0, len(byID))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if rec := byID[id]; rec != nil {
			out = append(out, rec)
		}
	}
	return out
}

func deductionRefLatestModelFromRecord(item DeductionRefLatestRecord) deductionRefLatestModel {
	return deductionRefLatestModel(item)
}

func attributeFiltersMatch(filters, attrs map[string][]string) bool {
	if len(filters) == 0 {
		return true
	}
	for key, wanted := range filters {
		got, ok := attrs[key]
		if !ok || len(got) == 0 || len(wanted) == 0 {
			return false
		}
		if !stringSlicesIntersect(wanted, got) {
			return false
		}
	}
	return true
}

func stringSlicesIntersect(left, right []string) bool {
	set := make(map[string]struct{}, len(left))
	for _, item := range left {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		set[item] = struct{}{}
	}
	for _, item := range right {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := set[item]; ok {
			return true
		}
	}
	return false
}

func generalVoucherScopeCondition(db *gorm.DB) string {
	if db != nil && db.Dialector != nil {
		switch db.Name() {
		case "postgres":
			return "attribute_filters = '{}'::jsonb"
		case "sqlite":
			return "COALESCE(CAST(attribute_filters AS TEXT), '') IN ('', '{}')"
		}
	}
	return "attribute_filters IS NULL OR attribute_filters = '{}'"
}

func generalVoucherBalanceSelect(db *gorm.DB) string {
	if db != nil && db.Dialector != nil {
		switch db.Name() {
		case "postgres":
			return "COALESCE(SUM(remaining_amount), 0)::text"
		case "sqlite":
			return "printf('%.12f', COALESCE(SUM(remaining_amount), 0))"
		}
	}
	return "COALESCE(SUM(remaining_amount), 0)"
}

func cursorOrderExpr(cursor CursorParams, createdColumn, idColumn string) string {
	if cursor.Order == "asc" {
		return createdColumn + " ASC, " + idColumn + " ASC"
	}
	return createdColumn + " DESC, " + idColumn + " DESC"
}

type createdIDCursorAnchor struct {
	CreatedAt time.Time
	ID        int64
}

type bssRechargeOrderRow struct {
	OrderID              string
	OrgID                string
	OrgType              string
	RequestedAmount      MoneyMicros
	PaidAmount           MoneyMicros
	DebtOffsetAmount     MoneyMicros
	Currency             string
	PaymentStatus        bssv1.PaymentOrderStatus
	Channel              bssv1.PaymentChannel
	ChannelOrderID       *string
	ChannelTransactionID *string
	WalletTransactionID  *string
	Remark               *string
	PaidAt               *time.Time
	CreatedAt            time.Time
	CreditedAt           *time.Time
	RefundableAmount     Money
}

type bssRefundOrderRow struct {
	RefundID             string
	OrgID                string
	OrgType              string
	PaymentOrderID       string
	Amount               MoneyMicros
	Currency             string
	Status               bssv1.RefundOrderStatus
	Reason               *string
	ActorID              *string
	ActorType            *string
	RefundChannel        *bssv1.PaymentChannel
	ChannelRefundID      *string
	ChannelTransactionID *string
	WalletTransactionID  *string
	ApprovedAt           *time.Time
	CompletedAt          *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ReviewerID           *string
	ReviewerNote         *string
}

func bssRechargeOrderRecordFromRow(row bssRechargeOrderRow) BSSRechargeOrderRecord {
	creditedAmount := row.PaidAmount.Sub(row.DebtOffsetAmount.Decimal)
	return BSSRechargeOrderRecord{
		OrderID:              row.OrderID,
		OrgID:                row.OrgID,
		OrgType:              row.OrgType,
		RequestedAmount:      row.RequestedAmount.Decimal,
		PaidAmount:           row.PaidAmount.Decimal,
		DebtOffsetAmount:     row.DebtOffsetAmount.Decimal,
		CreditedAmount:       creditedAmount,
		Currency:             row.Currency,
		Status:               bssRechargeStatusFromPaymentStatus(row.PaymentStatus),
		Channel:              row.Channel,
		ChannelOrderID:       stringFromPtr(row.ChannelOrderID),
		ChannelTransactionID: stringFromPtr(row.ChannelTransactionID),
		WalletTransactionID:  stringFromPtr(row.WalletTransactionID),
		PaidAt:               row.PaidAt,
		CreatedAt:            row.CreatedAt,
		CreditedAt:           row.CreditedAt,
		Remark:               stringFromPtr(row.Remark),
		RefundableAmount:     row.RefundableAmount,
	}
}

func bssRefundOrderRecordFromRow(row bssRefundOrderRow) BSSRefundOrderRecord {
	return BSSRefundOrderRecord{
		RefundID:             row.RefundID,
		OrgID:                row.OrgID,
		OrgType:              row.OrgType,
		PaymentOrderID:       row.PaymentOrderID,
		Amount:               row.Amount.Decimal,
		Currency:             row.Currency,
		Status:               row.Status,
		Reason:               stringFromPtr(row.Reason),
		ActorID:              stringFromPtr(row.ActorID),
		ActorType:            stringFromPtr(row.ActorType),
		RefundChannel:        paymentChannelFromPtr(row.RefundChannel),
		ChannelRefundID:      stringFromPtr(row.ChannelRefundID),
		ChannelTransactionID: stringFromPtr(row.ChannelTransactionID),
		WalletTransactionID:  stringFromPtr(row.WalletTransactionID),
		ApprovedAt:           row.ApprovedAt,
		CompletedAt:          row.CompletedAt,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
		ReviewerID:           stringFromPtr(row.ReviewerID),
		ReviewerNote:         stringFromPtr(row.ReviewerNote),
	}
}

func applyBSSRechargeOrderFilters(query *gorm.DB, filter ListBSSRechargeOrdersFilter) *gorm.DB {
	if filter.OrgType != "" {
		query = query.Where("payments.org_type = ?", filter.OrgType)
	}
	if statuses := paymentStatusesForBSSRechargeStatus(filter.Status); len(statuses) > 0 {
		query = query.Where("payments.status IN ?", statuses)
	}
	if filter.Channel != bssv1.PaymentChannel_PAYMENT_CHANNEL_UNSPECIFIED {
		query = query.Where("payments.channel = ?", filter.Channel)
	}
	if filter.RefundableOnly {
		query = applyRefundablePaymentOrderFilter(query)
	}
	return applyBSSSearchFilter(query, strings.TrimSpace(filter.Keyword), filter.OrgIDs, filter.SearchOrgIDs, []string{
		"payments.order_id",
		"payments.org_id",
		"payments.channel_transaction_id",
		"txs.transaction_id",
	}, "payments.org_id")
}

func applyRefundablePaymentOrderFilter(query *gorm.DB) *gorm.DB {
	return query.
		Where("payments.remaining_amount > payments.frozen_remaining_amount").
		Where("payments.status IN ?", []bssv1.PaymentOrderStatus{
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_PAID,
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_PARTIALLY_REFUNDED,
		})
}

func applyBSSRefundOrderFilters(query *gorm.DB, filter ListBSSRefundOrdersFilter) *gorm.DB {
	if filter.OrgType != "" {
		query = query.Where("refunds.org_type = ?", filter.OrgType)
	}
	if filter.Status != bssv1.RefundOrderStatus_REFUND_ORDER_STATUS_UNSPECIFIED {
		query = query.Where("refunds.status = ?", filter.Status)
	}
	return applyBSSSearchFilter(query, strings.TrimSpace(filter.Keyword), filter.OrgIDs, filter.SearchOrgIDs, []string{
		"refunds.refund_id",
		"refunds.payment_order_id",
		"refunds.org_id",
		"refunds.channel_refund_id",
		"payments.channel_transaction_id",
		"txs.transaction_id",
	}, "refunds.org_id")
}

func applyBSSSearchFilter(query *gorm.DB, keyword string, orgIDs []string, searchOrgIDs []string, columns []string, orgIDColumn string) *gorm.DB {
	normalizedSearchOrgIDs := normalizeSearchOrgIDs(searchOrgIDs)
	if keyword != "" {
		var clauses []string
		var args []any
		like := "%" + strings.ToLower(keyword) + "%"
		for _, column := range columns {
			clauses = append(clauses, "LOWER(COALESCE("+column+", '')) LIKE ?")
			args = append(args, like)
		}
		if len(normalizedSearchOrgIDs) > 0 {
			clauses = append(clauses, orgIDColumn+" IN ?")
			args = append(args, normalizedSearchOrgIDs)
		}
		query = query.Where("("+strings.Join(clauses, " OR ")+")", args...)
	} else if len(normalizedSearchOrgIDs) > 0 {
		query = query.Where(orgIDColumn+" IN ?", normalizedSearchOrgIDs)
	}
	normalizedOrgIDs := normalizeSearchOrgIDs(orgIDs)
	if len(normalizedOrgIDs) > 0 {
		query = query.Where(orgIDColumn+" IN ?", normalizedOrgIDs)
	}
	return query
}

func normalizeSearchOrgIDs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func bssRechargeStatusFromPaymentStatus(status bssv1.PaymentOrderStatus) bssv1.BSSRechargeOrderStatus {
	switch status {
	case bssv1.PaymentOrderStatus_PAYMENT_ORDER_PAID,
		bssv1.PaymentOrderStatus_PAYMENT_ORDER_REFUNDED,
		bssv1.PaymentOrderStatus_PAYMENT_ORDER_PARTIALLY_REFUNDED:
		return bssv1.BSSRechargeOrderStatus_BSS_RECHARGE_ORDER_PAID
	case bssv1.PaymentOrderStatus_PAYMENT_ORDER_CLOSED,
		bssv1.PaymentOrderStatus_PAYMENT_ORDER_EXPIRED,
		bssv1.PaymentOrderStatus_PAYMENT_ORDER_FAILED:
		return bssv1.BSSRechargeOrderStatus_BSS_RECHARGE_ORDER_CLOSED
	case bssv1.PaymentOrderStatus_PAYMENT_ORDER_PENDING:
		return bssv1.BSSRechargeOrderStatus_BSS_RECHARGE_ORDER_PENDING
	default:
		return bssv1.BSSRechargeOrderStatus_BSS_RECHARGE_ORDER_STATUS_UNSPECIFIED
	}
}

func paymentStatusesForBSSRechargeStatus(status bssv1.BSSRechargeOrderStatus) []bssv1.PaymentOrderStatus {
	switch status {
	case bssv1.BSSRechargeOrderStatus_BSS_RECHARGE_ORDER_PENDING:
		return []bssv1.PaymentOrderStatus{bssv1.PaymentOrderStatus_PAYMENT_ORDER_PENDING}
	case bssv1.BSSRechargeOrderStatus_BSS_RECHARGE_ORDER_PAID:
		return []bssv1.PaymentOrderStatus{
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_PAID,
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_REFUNDED,
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_PARTIALLY_REFUNDED,
		}
	case bssv1.BSSRechargeOrderStatus_BSS_RECHARGE_ORDER_CLOSED:
		return []bssv1.PaymentOrderStatus{
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_CLOSED,
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_EXPIRED,
			bssv1.PaymentOrderStatus_PAYMENT_ORDER_FAILED,
		}
	default:
		return nil
	}
}

func (r *gormWalletRepo) bssRechargeOrderQuery(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).
		Table("wallet.payment_orders AS payments").
		Joins(`LEFT JOIN wallet.wallet_transactions AS txs ON txs.id = (
SELECT txp.id
FROM wallet.wallet_transactions AS txp
WHERE txp.reference_type = ? AND txp.reference_id = payments.order_id AND txp.tx_type = ?
ORDER BY txp.created_at DESC, txp.id DESC
LIMIT 1
)`, "payment_order", bssv1.TransactionType_RECHARGE)
}

func (r *gormWalletRepo) bssRefundOrderQuery(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).
		Table("wallet.refund_orders AS refunds").
		Joins("JOIN wallet.payment_orders AS payments ON payments.order_id = refunds.payment_order_id AND payments.org_id = refunds.org_id").
		Joins(`LEFT JOIN wallet.wallet_transactions AS txs ON txs.id = (
SELECT txp.id
FROM wallet.wallet_transactions AS txp
WHERE txp.reference_type = ? AND txp.reference_id = refunds.refund_id AND txp.tx_type IN ?
ORDER BY CASE WHEN txp.tx_type = ? THEN 0 ELSE 1 END, txp.created_at DESC, txp.id DESC
LIMIT 1
)`, "refund_order", []bssv1.TransactionType{bssv1.TransactionType_REFUND, bssv1.TransactionType_FREEZE}, bssv1.TransactionType_REFUND)
}

func applyCreatedIDCursor(base func() *gorm.DB, businessIDColumn string, cursor CursorParams) (*gorm.DB, error) {
	query := base()
	for _, item := range []struct {
		id        string
		direction string
	}{
		{id: cursor.After, direction: "after"},
		{id: cursor.Before, direction: "before"},
	} {
		if item.id == "" {
			continue
		}
		anchor, ok, err := loadCreatedIDCursorAnchor(base(), businessIDColumn, item.id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return query.Where("1 = 0"), nil
		}
		query = query.Where(createdIDCursorCondition(cursor.Order, item.direction), anchor.CreatedAt, anchor.CreatedAt, anchor.ID)
	}
	return query, nil
}

func applyAliasedCreatedIDCursor(base func() *gorm.DB, businessIDColumn, createdColumn, idColumn string, cursor CursorParams) (*gorm.DB, error) {
	query := base()
	for _, item := range []struct {
		id        string
		direction string
	}{
		{id: cursor.After, direction: "after"},
		{id: cursor.Before, direction: "before"},
	} {
		if item.id == "" {
			continue
		}
		anchor, ok, err := loadAliasedCreatedIDCursorAnchor(base(), businessIDColumn, createdColumn, idColumn, item.id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return query.Where("1 = 0"), nil
		}
		query = query.Where(aliasedCreatedIDCursorCondition(cursor.Order, item.direction, createdColumn, idColumn), anchor.CreatedAt, anchor.CreatedAt, anchor.ID)
	}
	return query, nil
}

func loadAliasedCreatedIDCursorAnchor(query *gorm.DB, businessIDColumn, createdColumn, idColumn, value string) (createdIDCursorAnchor, bool, error) {
	var anchor createdIDCursorAnchor
	err := query.Select(createdColumn+" AS created_at, "+idColumn+" AS id").Where(businessIDColumn+" = ?", value).Take(&anchor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return createdIDCursorAnchor{}, false, nil
	}
	if err != nil {
		return createdIDCursorAnchor{}, false, err
	}
	return anchor, true, nil
}

func aliasedCreatedIDCursorCondition(order, direction, createdColumn, idColumn string) string {
	forward := order == "asc"
	if direction == "before" {
		forward = !forward
	}
	if forward {
		return "(" + createdColumn + " > ? OR (" + createdColumn + " = ? AND " + idColumn + " > ?))"
	}
	return "(" + createdColumn + " < ? OR (" + createdColumn + " = ? AND " + idColumn + " < ?))"
}

func loadCreatedIDCursorAnchor(query *gorm.DB, businessIDColumn, value string) (createdIDCursorAnchor, bool, error) {
	var anchor createdIDCursorAnchor
	err := query.Select("created_at, id").Where(businessIDColumn+" = ?", value).Take(&anchor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return createdIDCursorAnchor{}, false, nil
	}
	if err != nil {
		return createdIDCursorAnchor{}, false, err
	}
	return anchor, true, nil
}

func createdIDCursorCondition(order, direction string) string {
	forward := order == "asc"
	if direction == "before" {
		forward = !forward
	}
	if forward {
		return "(created_at > ? OR (created_at = ? AND id > ?))"
	}
	return "(created_at < ? OR (created_at = ? AND id < ?))"
}
