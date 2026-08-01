# Solidarity Tech Integration

API-driven integration with [Solidarity Tech](https://solidarity.tech) (a CRM / member platform).
Exposes pipeline actions that look up SolidarityTech user profiles and update their custom
properties, tying profiles to member records in this app.

---

## What it does

Two actions, available in the webhook/event trigger builder:

- **Look up SolidarityTech profile** (`solidaritytech.lookup_user`) — resolves a member from a
  provider identity (e.g. Slack + `{{user_id}}`), finds their SolidarityTech profile by email,
  caches the SolidarityTech profile id on the member, and emits profile fields for downstream
  steps (`{{solidaritytech_user_id}}`, `{{solidaritytech_email}}`, …).
- **Set SolidarityTech custom property** (`solidaritytech.set_custom_property`) — sets a custom
  property value on a SolidarityTech profile via `PUT /users/{id}`, e.g. write a member's Slack
  user id into a `slack_user_id` custom property.

### Profile ↔ member mapping

SolidarityTech profile ids are cached on member records using the existing `user_identities`
table with `provider='solidaritytech'` (`external_id` = the SolidarityTech user id). The first
`lookup_user` run for a member looks them up by email and records the mapping; subsequent runs
skip the email lookup and read the cached id directly.

---

## Example flow

When a member joins Slack, write their Slack user id onto their SolidarityTech profile:

1. Trigger: **Member joins workspace** (`slack.team_join`) — fires with `{{user_id}}`.
2. Action: **Look up SolidarityTech profile** — `provider` = Slack, `external_id` = `{{user_id}}`.
3. Action: **Set SolidarityTech custom property** — `user_id` = `{{solidaritytech_user_id}}`,
   `property_key` = `slack_user_id`, `value` = `{{user_id}}`.

---

## Setup

### 1. Get a Solidarity Tech API key

In your Solidarity Tech account, go to **Settings → API Keys** and generate a key. (API access
is role-gated; ask your tech lead or an admin if you don't see that section.)

### 2. Configure the key

Go to **Integrations → Solidarity Tech** in the admin panel and set **API Key**.

### 3. Create the custom property in Solidarity Tech

Create the custom property you want to write (e.g. `slack_user_id`) under Solidarity Tech's
custom user properties. Use its **internal name** as the action's `property_key`.

---

## Config Keys Reference

| Service | Key | Sensitive | Description |
|---|---|---|---|
| `solidaritytech` | `api_key` | Yes | Solidarity Tech API key (Bearer token) |

---

## Action parameters

### Look up SolidarityTech profile

| Param | Type | Required | Description |
|---|---|---|---|
| `provider` | select | yes | The member identity provider: Slack, Discord, Matrix, or Web (email). |
| `external_id` | text (dynamic) | yes | The member's external id for that provider, e.g. `{{user_id}}`. |

Outputs: `solidaritytech_user_id`, `solidaritytech_hash_id`, `solidaritytech_email`,
`solidaritytech_first_name`, `solidaritytech_last_name`, `member_id`, `member_email`.

### Set SolidarityTech custom property

| Param | Type | Required | Description |
|---|---|---|---|
| `user_id` | text (dynamic) | yes | SolidarityTech profile id, e.g. `{{solidaritytech_user_id}}`. |
| `property_key` | text | yes | The property's `internal_name`, e.g. `slack_user_id`. |
| `value` | text (dynamic) | yes | The value to set, e.g. `{{user_id}}`. |
| `append` | boolean | no | For Multiple-Checkboxes: merge (true) vs replace (false, default). |

---

## Architecture Notes

- **API**: `https://api.solidarity.tech/v1`, Bearer auth, 60 requests / 30 seconds. The client
  returns a clear error on `429`; no client-side throttling (pipeline usage is far below the
  limit).
- **Rate of mapping**: lazy. The `solidaritytech` identity is written on the first
  `lookup_user` run for a member. A backfill sync to pre-populate mappings is a future follow-up.
- **Custom property key**: free text (the property's `internal_name`) for now. A dynamic
  dropdown populated from `GET /custom_user_properties` is a future enhancement.
