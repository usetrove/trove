package redact

import "regexp"

// reMoreShapes holds other well-known credential shapes, so they are redacted even with no sensitive name beside
// them (finding F-26): AWS access key ids, GitHub tokens, Google API keys, Slack tokens, Stripe keys, npm tokens
// and PEM private key blocks. It is best effort, and the list can stay short because a shape it does not know is
// still caught next to a sensitive name (reKV). An unterminated private key block (a log cut off in the middle)
// is redacted to the end of the text.
var reMoreShapes = regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA)[A-Z0-9]{16}\b` +
	`|\bgh[pousr]_[A-Za-z0-9]{30,}|\bgithub_pat_[A-Za-z0-9_]{30,}` +
	`|\bAIza[0-9A-Za-z_\-]{35}` +
	`|\bxox[abpors]-[A-Za-z0-9\-]{10,}` +
	`|\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{10,}` +
	`|\bnpm_[A-Za-z0-9]{36}\b` +
	`|-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`)
