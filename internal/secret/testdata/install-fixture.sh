#!/bin/sh
# A stand-in for anywaye/kit/templates/substrate/install.sh, carrying the exact
# statements internal/secret cites. It is a fixture so the citation guard can be
# driven — survives a move, fails on a deletion — without editing kit, which
# lives in another repository and moves under this test.
#
# --- step 9: Redis and the services' secrets ---
install_redis_and_secrets() {
  if ! kubectl --kubeconfig="$KUBECONFIG" get secret redis-password -n data >/dev/null 2>&1; then
    kubectl --kubeconfig="$KUBECONFIG" create secret generic redis-password -n data \
      --from-literal=password="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')" >/dev/null
  fi
}

# SECRET RECORD (the drift tripwire — tests/substrate_test.sh parses these:
# every name here must be created below, and every key must appear as a
# --from-literal below; a fixture with one removed must fail):
#   RECORD identity-secrets: DATABASE_URL SIGNUP_SIGNING_KEY MFA_ENCRYPTION_KEY NATS_URL
#   RECORD gateway-secrets: REDIS_URL
#   RECORD courier-secrets: DATABASE_URL SECRET_KEY_BASE COURIER_IDENTITY_TOKEN COURIER_SECRET_BOX_KEY
#   RECORD parlor-secrets: DATABASE_URL
#
# THE SPLIT IS BY PROVENANCE:
#   generated on the box, never printed, generated ONCE and read back —
#     SIGNUP_SIGNING_KEY and MFA_ENCRYPTION_KEY,
#   minted on the box in step 11, never printed, never an input —
#     COURIER_IDENTITY_TOKEN,
#   from the environment, shape-validated, never echoed — courier's SMTP
#     settings and the provider's signing secret,
#   NEITHER — configuration that rides a secret-shaped slot.
#
# `DATABASE_URL` agrees with the Cluster by construction — the password inside
# it is the Postgres role password step 8 generates.
# Generated here: SECRET_KEY_BASE (64 bytes — Phoenix refuses shorter).
# Role passwords: generated ONCE, read back on a re-run.
# The password is read back from step 9's secret.
# The signing key is generated here and never printed.
# refusing to overwrite a pre-existing identity-secrets.
# rotate by replacing the secret and restarting the pods.
