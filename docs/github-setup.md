# GitHub Integration Setup

FeatherDeploy supports two complementary GitHub integrations. You can use one or both depending on your needs.

| Integration | Purpose | Recommended for |
|---|---|---|
| **GitHub App** | Organisation/account-wide repo access via installation tokens | Teams, shared organisations |
| **GitHub OAuth App** | Per-user GitHub account connection | Individual users browsing their own repos |

> **Quick summary of URLs for `https://hosting.intelectio.art`**
>
> | Endpoint | URL |
> |---|---|
> | OAuth callback | `https://hosting.intelectio.art/api/github/callback` |
> | App webhook | `https://hosting.intelectio.art/api/github-app/webhook` |

---

## Part 1 — GitHub App

A GitHub App gives FeatherDeploy installation-level access to repositories. It uses short-lived RSA-signed JWTs instead of long-lived tokens, and doesn't require every user to connect their personal account.

### Step 1 — Create the App

1. Open **GitHub → Settings → Developer settings → GitHub Apps → New GitHub App**
   (direct link: [https://github.com/settings/apps/new](https://github.com/settings/apps/new))

2. Fill in the form exactly as shown below:

   | Field | Value |
   |---|---|
   | **GitHub App name** | `FeatherDeploy` (must be globally unique — add a suffix if taken) |
   | **Description** | `Self-hosted PaaS deployment panel` |
   | **Homepage URL** | `https://hosting.intelectio.art` |
   | **Callback URL** | `https://hosting.intelectio.art/api/github/callback` |
   | ☐ Expire user authorization tokens | Leave **unchecked** |
   | ☐ Request user authorization (OAuth) during installation | Leave **unchecked** |
   | ☐ Enable Device Flow | Leave **unchecked** |
   | **Setup URL** | Leave blank |
   | ☐ Redirect on update | Leave **unchecked** |

3. **Webhook section:**

   | Field | Value |
   |---|---|
   | **Active** | ✅ **Check this** |
   | **Webhook URL** | `https://hosting.intelectio.art/api/github-app/webhook` |
   | **Secret** | Generate a random string and save it — for example, run `openssl rand -hex 32` in a terminal |

   > FeatherDeploy verifies every webhook delivery using HMAC-SHA256 (`X-Hub-Signature-256`). Never leave the webhook secret empty on a production installation.

4. **Repository permissions** — expand the section and set:

   | Permission | Access |
   |---|---|
   | **Contents** | Read-only |
   | **Metadata** | Read-only *(auto-selected)* |

   You do **not** need any Organisation or Account permissions for basic repo browsing.

5. **Subscribe to events:**

   | Event | Why |
   |---|---|
   | ✅ **Push** | Logs push events; used for future auto-deploy on commit |
   | ✅ **Meta** | Notifies when the App itself is deleted |

6. **Where can this GitHub App be installed?**
   - Choose **Any account** — allowing any GitHub user/org to install FeatherDeploy.
   - Choose **Only on this account** if this panel is private to your org.

7. Click **Create GitHub App**.

---

### Step 2 — Note the App ID

After creation, you land on the App's settings page. The **App ID** is shown at the top (a number like `1234567`). Write it down — you will need it in Step 5.

---

### Step 3 — Generate a Private Key

1. Scroll to the bottom of the App settings page.
2. Under **Private keys**, click **Generate a private key**.
3. A `.pem` file is downloaded automatically. This is the **only copy** — GitHub does not store it. Keep it safe.
4. Open the file in a text editor. It starts with:
   ```
   -----BEGIN RSA PRIVATE KEY-----
   ...
   -----END RSA PRIVATE KEY-----
   ```
   You will paste the full contents into FeatherDeploy in Step 5.

---

### Step 4 — Install the App

1. On the App settings page, click **Install App** in the left sidebar.
2. Click **Install** next to your account or organisation.
3. Choose which repositories to grant access to:
   - **All repositories** — FeatherDeploy can see everything (recommended for simplicity).
   - **Only select repositories** — pick specific repos.
4. Click **Install**.
5. After installation the browser URL changes to:
   ```
   https://github.com/settings/installations/XXXXXXXX
   ```
   The number at the end is your **Installation ID**. Note it.

   You can also find it later at **GitHub → Settings → Applications → Installed GitHub Apps → FeatherDeploy → Configure** — the URL will again show the installation ID.

---

### Step 5 — Configure FeatherDeploy

Log in to FeatherDeploy as a **superadmin**, go to **Admin Settings**, and find the **GitHub App** section. Fill in:

| Field | Where to find it |
|---|---|
| **App ID** | App settings page (Step 2) |
| **App Name** | The name you chose (e.g. `FeatherDeploy`) |
| **Private Key (PEM)** | Full contents of the `.pem` file downloaded in Step 3 |
| **Installation ID** | URL after clicking Install (Step 4) |
| **Webhook Secret** | The random string you generated in Step 1 |
| **Client ID** *(optional)* | App settings page — shown under "Client ID" |
| **Client Secret** *(optional)* | App settings page — generate under "Client secrets" |

> **Client ID / Client Secret on a GitHub App** are only needed if you want the App to handle user-level OAuth (connecting personal accounts). If you are setting up a separate GitHub OAuth App (Part 2), you can skip these fields.

Click **Save** in FeatherDeploy. GitHub App repos will now appear in the repo selector when creating or editing services.

---

## Part 2 — GitHub OAuth App

The OAuth App lets individual users connect their **personal GitHub account** to FeatherDeploy so they can browse repos and private repos they personally have access to.

### Step 1 — Create the OAuth App

1. Go to **GitHub → Settings → Developer settings → OAuth Apps → New OAuth App**
   (or the Admin Settings page in FeatherDeploy shows the link)

2. Fill in the form:

   | Field | Value |
   |---|---|
   | **Application name** | `FeatherDeploy` |
   | **Homepage URL** | `https://hosting.intelectio.art` |
   | **Authorization callback URL** | `https://hosting.intelectio.art/api/github/callback` |

3. Click **Register application**.

---

### Step 2 — Copy the Credentials

1. On the app page, copy the **Client ID** — it looks like `Ov23liXXXXXXXXXX`.
2. Click **Generate a new client secret** and copy it immediately. GitHub shows it only once.

---

### Step 3 — Configure FeatherDeploy

Log in as **superadmin** → **Admin Settings → GitHub OAuth** section:

| Field | Value |
|---|---|
| **Client ID** | From Step 2 |
| **Client Secret** | From Step 2 |

Click **Save credentials**. Users can now go to their profile and click **Connect GitHub** to link their account.

---

## Webhook Reference

| Property | Value |
|---|---|
| **Webhook URL** | `https://hosting.intelectio.art/api/github-app/webhook` |
| **Content type** | `application/json` |
| **Secret** | The value you entered when creating the App |
| **SSL verification** | ✅ Enabled (required — FeatherDeploy uses HTTPS) |
| **Events** | Push, Meta (or "Let me select individual events") |

**How signature verification works:**

GitHub signs every delivery with HMAC-SHA256 using your webhook secret and includes the result in the `X-Hub-Signature-256` header:

```
X-Hub-Signature-256: sha256=<hex>
```

FeatherDeploy rejects any delivery where this signature is missing or does not match. Always set a webhook secret.

**Events handled:**

| Event | What FeatherDeploy does |
|---|---|
| `push` | Logs the repo, branch and commit SHA. *(Auto-deploy trigger — coming in a future release.)* |
| `ping` | Acknowledges the webhook was set up correctly. |
| Everything else | Acknowledged with `204 No Content` and logged for debugging. |

---

## Finding Your Installation ID Later

If you need the Installation ID after the initial setup:

1. On GitHub, go to **Settings → Applications → Installed GitHub Apps**.
2. Click **Configure** next to FeatherDeploy.
3. The URL in your browser will be:
   ```
   https://github.com/settings/installations/XXXXXXXX
   ```
   The number at the end is your Installation ID.

Alternatively, if you have the App's private key, you can query the GitHub API:

```bash
# 1 — Create a JWT (requires the jwt-cli tool or any JWT library)
# 2 — List installations
curl -H "Authorization: Bearer <app-jwt>" \
     -H "Accept: application/vnd.github+json" \
     https://api.github.com/app/installations
```

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| "GitHub integration not configured" when users try to connect | Set GitHub OAuth Client ID + Secret in Admin Settings → GitHub OAuth |
| GitHub App repos not loading | Verify App ID, Installation ID, and that the `.pem` key matches the App |
| `could not obtain GitHub installation token` | The private key is wrong or expired — regenerate it on GitHub and update |
| Webhook deliveries failing with 401 | Webhook secret in FeatherDeploy doesn't match the one set on GitHub |
| Webhook deliveries failing with 404 | The URL should be exactly `https://hosting.intelectio.art/api/github-app/webhook` |
| Installation URL not visible | Go to github.com → Settings → Applications → Installed GitHub Apps → Configure |
| "App name already taken" | GitHub App names are globally unique — append your handle, e.g. `FeatherDeploy-ojhapranjal26` |
