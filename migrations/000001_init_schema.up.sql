CREATE TABLE user_wallets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id VARCHAR(64) UNIQUE NOT NULL,
    active_balance DECIMAL(18,8) NOT NULL DEFAULT 0,
    blocked_balance DECIMAL(18,8) NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE ledger_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id VARCHAR(64) NOT NULL,
    amount DECIMAL(18,8) NOT NULL,
    type VARCHAR(20) NOT NULL, -- BLOCK, UNBLOCK, PLATFORM_FEE, WORKER_PAYOUT
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE worker_wallets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    worker_id VARCHAR(64) UNIQUE NOT NULL,
    owner_email VARCHAR(255),
    wallet_address VARCHAR(128),
    bank_iban VARCHAR(34),
    pending_balance DECIMAL(18,8) NOT NULL DEFAULT 0,
    confirmed_balance DECIMAL(18,8) NOT NULL DEFAULT 0,
    total_earned DECIMAL(18,8) NOT NULL DEFAULT 0,
    total_withdrawn DECIMAL(18,8) NOT NULL DEFAULT 0,
    is_frozen BOOLEAN NOT NULL DEFAULT FALSE,
    frozen_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE worker_earnings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    worker_id VARCHAR(64) NOT NULL REFERENCES worker_wallets(worker_id),
    job_id VARCHAR(64) NOT NULL,
    gross_amount DECIMAL(18,8) NOT NULL,
    platform_fee DECIMAL(18,8) NOT NULL,
    net_amount DECIMAL(18,8) NOT NULL,
    compute_time_ms BIGINT NOT NULL,
    gpu_model VARCHAR(64),
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    spot_check_id UUID,
    confirmed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE withdrawal_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    worker_id VARCHAR(64) NOT NULL REFERENCES worker_wallets(worker_id),
    amount DECIMAL(18,8) NOT NULL,
    currency VARCHAR(10) NOT NULL DEFAULT 'USDT',
    payout_method VARCHAR(20) NOT NULL,
    destination VARCHAR(256) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    tx_hash VARCHAR(128),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);
