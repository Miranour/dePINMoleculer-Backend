CREATE TABLE jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    smiles_string TEXT NOT NULL,
    target_pdb_url TEXT NOT NULL,
    target_pdb_id VARCHAR(10),
    max_exhaustiveness INT NOT NULL DEFAULT 8,
    cost DECIMAL(18,8) NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'QUEUED',
    result_energy DECIMAL(10,4),
    result_pdbqt_url TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_jobs_user_id ON jobs(user_id);
CREATE INDEX idx_jobs_status ON jobs(status);
