# GCP setup brief — for the agent with full gcloud auth

## Objective
Create an **isolated** Google Cloud project + a **scoped, budget-capped service-account key** so the C3 *build* agent can deploy and run the **Telegram reverse-proxy VM** (Compute Engine e2-micro running an nginx Bot-API reverse-proxy + an mtg MTProto proxy) — WITHOUT giving the build agent any access to Karthi's other projects, VMs, or production.

Context: Telegram's IP ranges are null-routed on Karthi's Indian network, so C3's broker can't reach `api.telegram.org`. The fix is a maintainer-owned reverse proxy on a VM **outside India**. This brief provisions the key; the C3 build agent then runs the deploy itself (see `docs/DEPLOY-telegram-proxy.md`).

## HARD REQUIREMENT — isolation (read first)
- Create a **brand-new** project dedicated to this. **Do NOT reuse any existing or production project** (not the proctor project, not anything else).
- The deployer service account must be a member of **ONLY this new project** (GCP IAM is per-project; a SA scoped here cannot see or touch any other project/VM).
- **Do NOT** grant any org-level or folder-level roles. **Do NOT** hand over Karthi's personal user credentials — produce a **service-account key** scoped to this project only.
- The result must be **budget-capped and deletable**.

## Prereqs (you, the setup agent)
- `gcloud` authenticated as Karthi with rights to create projects + link billing.
- Karthi's billing account id: `gcloud billing accounts list`.

## Steps
```bash
# --- variables ---
PROJECT_ID="<your-project-id>"              # IDs are GLOBALLY unique, 6-30 chars, lowercase/digits/hyphen.
                                            # If taken, append a short suffix, e.g. tg-proxy-7k2.
REGION="<non-india-region>"                 # MUST be a NON-INDIA, GCP always-free-tier-eligible region
                                            # (see cloud.google.com/free for the eligible US regions). A
                                            # Mumbai (asia-south1) VM is on an Indian network and blocked too — never use it.
ZONE="<non-india-zone>"
SA="proxy-deployer"
KEY_PATH="$HOME/.config/c3/gcp-proxy-sa.json"   # OUTSIDE the c3 repo (never committed)

# --- 1. create the isolated project ---
gcloud projects create "$PROJECT_ID" --name="C3 Telegram Proxy"

# --- 2. link billing + a SMALL budget (the e2-micro is free-tier, but cap cost as a backstop) ---
BILLING=$(gcloud billing accounts list --format='value(name)' --filter='open=true' | head -1)
gcloud billing projects link "$PROJECT_ID" --billing-account="$BILLING"
# RECOMMENDED: set a small budget + alert (Console → Billing → Budgets & alerts, e.g. $15/mo with
# 50/90/100% email alerts) so a runaway (e.g. egress overage) cannot run up cost. The intended
# steady-state cost is ~$0 (one e2-micro in a free-tier US region + <1GB/mo egress).

# --- 3. enable the APIs the deploy needs (Compute + SSH/OS Login + IAM) ---
gcloud services enable \
  compute.googleapis.com oslogin.googleapis.com \
  iamcredentials.googleapis.com cloudresourcemanager.googleapis.com \
  --project="$PROJECT_ID"

# --- 4. dedicated deployer SA, OWNER on THIS project ONLY ---
gcloud iam service-accounts create "$SA" --project="$PROJECT_ID" \
  --display-name="C3 Telegram-proxy deployer (build agent)"
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role="roles/owner" --condition=None
# (Tighter alternative to roles/owner, if preferred — grant each instead:
#   roles/compute.admin              (create the VM, firewall, static IP)
#   roles/compute.osAdminLogin       (SSH in via OS Login to install nginx/mtg + certs)
#   roles/iam.serviceAccountUser     (attach the default compute SA to the VM)
#   roles/serviceusage.serviceUsageAdmin
#  Owner-on-an-isolated-deletable-project gives the same isolation with less fuss.)

# --- 5. create the key the build agent will use ---
mkdir -p "$(dirname "$KEY_PATH")"
gcloud iam service-accounts keys create "$KEY_PATH" \
  --iam-account="${SA}@${PROJECT_ID}.iam.gserviceaccount.com"
chmod 600 "$KEY_PATH"

# --- 6. verify the KEY works on its own (not via Karthi's login) ---
gcloud auth activate-service-account --key-file="$KEY_PATH"
gcloud config set project "$PROJECT_ID"
gcloud projects describe "$PROJECT_ID" --format='value(projectId)'   # should print the project id
gcloud compute regions describe "$REGION" --format='value(name)'     # confirms compute API + region access
# IMPORTANT: switch gcloud's active account back to Karthi afterward if you keep using this shell:
#   gcloud config set account <karthi@...>
```

## Deliverable / handoff to the C3 build agent
On the **same machine as the C3 repo / build agent** (Karthi's laptop), write the env file the build agent reads. It lives **outside the repo** so the key can never be committed:
```bash
mkdir -p "$HOME/.config/c3"
cat > "$HOME/.config/c3/gcp-proxy.env" <<EOF
GCP_PROJECT_ID=$PROJECT_ID
GCP_REGION=$REGION
GCP_ZONE=$ZONE
GOOGLE_APPLICATION_CREDENTIALS=$KEY_PATH
EOF
chmod 600 "$HOME/.config/c3/gcp-proxy.env"
```
- If you ran this on a **different machine** than the C3 build, copy `$KEY_PATH` onto the build machine and set `GOOGLE_APPLICATION_CREDENTIALS` to its path there.
- Ensure the **`gcloud` CLI is on the build machine** (Karthi's is at `~/google-cloud-sdk/bin`) — the build agent runs `gcloud auth activate-service-account --key-file=$GOOGLE_APPLICATION_CREDENTIALS` then the deploy.
- Report back to Karthi: the `PROJECT_ID`, `REGION`, that `~/.config/c3/gcp-proxy.env` + the key file are in place, and the budget is set. Then Karthi tells the build agent "the key is ready," and it runs the deploy.

## What you should NOT do
- **Do not create the VM, firewall, IP, or install anything** — the C3 build agent does the entire deploy (`docs/DEPLOY-telegram-proxy.md`) using this key, because it needs to interleave with Karthi pointing the two subdomains' DNS and obtaining the Let's Encrypt certs. Your job is only: project + billing + budget + APIs + SA + key + handoff.
- **Do not put the bot token anywhere** — the Telegram bot token is C3's secret and never touches this provisioning step. The deploy passes it only at runtime, TLS-verified, from C3 → the VM → Telegram.
- Do not grant org/folder roles, reuse a prod project, or expose Karthi's user credentials.

## Cleanup (after we're done / tearing the proxy down)
```bash
# delete the VM + firewall + static IP first (the build agent can, or:)
gcloud compute instances delete <your-proxy-vm> --zone "$ZONE" --quiet || true
# then the key + the whole project
gcloud iam service-accounts keys list --iam-account="${SA}@${PROJECT_ID}.iam.gserviceaccount.com"
gcloud iam service-accounts keys delete <KEY_ID> --iam-account="${SA}@${PROJECT_ID}.iam.gserviceaccount.com"
gcloud projects delete "$PROJECT_ID"
```
```
