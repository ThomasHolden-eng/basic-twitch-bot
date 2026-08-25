# Go Twitch Bot

A modular Twitch chatbot written in Go. The bot authenticates via the
Twitch OAuth2 Authorization Code flow, joins a single channel over IRC,
subscribes to channel-point redemptions over EventSub, and dispatches
chat messages and redemption events to handlers. State persistence is
managed by a local SQLite database (`state.db`).

The bot is intended to be operated by a streamer who runs the bot on
a machine of their own. The bot account and the channel account are usually
two different Twitch accounts, but this is not a requirement.

The repository ships without any commands registered - `dispatch.go`
is intentionally left for you to fill in. See [Adding Commands](#adding-commands)
below.

## 1. Register a Twitch Application on the broadcaster

The bot requires authenticaion through a Twitch application that you 
register under your **channel** (the account that owns the stream you
want the bot to join).

1. Open the [Twitch Developer Console](https://dev.twitch.tv/console).
2. Click **Register Your Application**.
3. Fill in:
   - **Name:** something unique.
   - **OAuth Redirect URLs:** `http://localhost:8080/callback`
   - **Category:** Chat Bot
   - **Client Type:** Confidential
4. Click **Create**, then **Manage** on the new application.
5. Copy the **Client ID** and generate a **Client Secret**. You will
   paste both into `config.json` in the next step.

## 2. Register a Twitch Application on the bot

The bot also requires authentication on its own account through a 
Twitch application that you register under your **bot** account.

1. Open the [Twitch Developer Console](https://dev.twitch.tv/console).
2. Click **Register Your Application**.
3. Fill in:
   - **Name:** something unique.
   - **OAuth Redirect URLs:** `http://localhost:8080/callback`
   - **Category:** Chat Bot
   - **Client Type:** Confidential
4. Click **Create**, then **Manage** on the new application.
5. Copy the **Client ID** and generate a **Client Secret**. You will
   paste both into `config.json` in the next step.

> If you already have a different Twitch application for the bot
> account, you can register the bot account's application instead -
> the redirect URL just has to match. The same `client_id` and
> `client_secret` go into `config.json` regardless of which account
> registered the application.

## 3. Fill in `config.json`

The bot reads its configuration from `config.json` in the working
directory. On first run, if the file does not exist, a placeholder is
written and the bot exits with an instructive message. Edit the
placeholder with your real values before running again.

| Field | Required | Description |
| --- | --- | --- |
| `username` | yes | The Twitch account the **bot** logs in as. Usually a separate account from the channel. |
| `client_id` | yes | The bot's Client ID from the Twitch Developer Console (step 2). |
| `client_secret` | yes | The bot's Client Secret from the Twitch Developer Console (step 2). |
| `broadcaster_id` | yes | Your channel's Client ID from the Twitch Developer Console (step 1). |
| `broadcaster_secret` | yes | The bot's Client Secret from the Twitch Developer Console (step 1). |
| `channel` | yes | The login name of the channel the bot should join. |
| `fyrewire_key` | no | Reserved for future use. |
| `val_user`, `val_tag`, `val_region` | no | Reserved for future use. |
| `discord_link`, `tiktok_link`, `instagram_link`, `twitter_link`, `youtube_link`, `donate_link` | no | Reserved for future use. |
| `nickname` | no | Reserved for future use. |

Example:

```json
{
  "username": "my_bot_account",
  "client_id": "ab1cdef23ghijk45lmnopq67rstu",
  "client_secret": "abc1d2efg45hijk67lmnopqrstuvwx",
  "broadcaster_id": "987654321",
  "broadcaster_secret": "zxy1w2vut3srqponm8lkjihgfedcba",
  "channel": "the_streamer_channel",
  "fyrewire_key": "",
  "val_user": "",
  "val_tag": "",
  "val_region": "",
  "discord_link": "",
  "tiktok_link": "",
  "instagram_link": "",
  "twitter_link": "",
  "youtube_link": "",
  "donate_link": "",
  "nickname": ""
}
```

All values must be lower case where applicable. Twitch login names are
case-insensitive at the protocol level but the bot stores them as
written.

## 4. Run the Bot

Requires Go 1.25 or newer.

From the repository root:

```
go run .
```

On first run for each `client_id`, the bot opens your default browser
to a Twitch authorization page. **Log in with the bot account** (the
one in `config.json`'s `username` field) and click **Authorize**. You
will be redirected to a page that says "Authentication Successful!" -
close that tab.

A second browser window opens for the broadcaster's EventSub
authorization - log in there **with the channel owner account**.

Once both OAuth flows succeed, the bot joins the channel and starts
the EventSub subscription. Logs go to `log.txt` (rotated to
`old_log.txt` on each launch) and to stdout.

To stop the bot, press Ctrl-C. The bot drains its goroutines and
exits cleanly.

## Adding Commands

Commands are not included in the repository. To add one, write a
function with the signature

```go
func(h *command.CommandHandler, user chat.User, args []string) (string, error)
```

Returning a non-empty string sends it to chat as the bot. Register it
in `internal/command/dispatch.go`'s `RegisterCommands`:

```go
h.Register("hello", func(h *command.CommandHandler, user chat.User, args []string) (string, error) {
    return fmt.Sprintf("hello, @%s!", user.Name), nil
})
```

Commands have access to:

- `h.config` - the `SafeConfig` loaded from `config.json`.
- `h.broadcaster` - the resolved `*api.TwitchUser` for the channel
  owner (used for `h.broadcaster.ID` in Helix calls that require the
  broadcaster's user ID).
- `h.database` - a `*state.KVStorage` for persisting counters and
  per-user state across restarts.
- `h.apiClient` - for Helix API calls and sending chat messages.
- `h.ChannelName()` and `h.Nickname()` - convenience accessors that
  return empty / fallback values rather than panicking on a nil
  broadcaster.

Channel-point redemptions are registered separately with
`h.RegisterRedeem("reward title", fn)` and trigger when a viewer
redeems a custom reward whose title matches.

## Future Work

Today, adding or changing a command requires editing
`internal/command/dispatch.go` and rebuilding the binary. A planned
improvement is to lift the command registry out of compile-time
constants: store command definitions - name, response template,
enabled flag - as runtime variables loaded from a config file or a
database table, so a streamer can add or tweak commands without
recompiling and redeploying the bot. The existing
`command.CommandHandler.Register` API is already shaped to support
this; only the source of the registration list needs to change.