# Go Twitch Bot

This is a modular and lightweight Twitch bot written in Go. It uses a secure, browser-based authentication flow to connect to your Twitch account.

## 1. Register a Twitch Application

Before you can use the bot, you need to register it as an "application" on Twitch to get a Client ID.

Go to the [Twitch Developer Console](https://dev.twitch.tv/console) and log in with the account you wish for the bot to operate for, i.e. your main twitch channel.
Look at Applications on the right, then click Register Your Application.

Fill out the form:
Name: Give your bot a unique name.
OAuth Redirect URLs: http://localhost:8080/callback
Category: Chat Bot
Client Type: Confidential
Click Create.

You will now see your application's details. Press manage, and copy the Client ID and generate a Client Secret. You will need them in the next step.

## 2. Configure the Bot

When you first run the bot, it will create a config.json file in the same directory.
Open this file and fill in the details:

username: The username of the Twitch account the bot will use to send messages.
client_id: The Client ID you copied from the Twitch Developer Console in the previous step.
client_password: The Client Password also copied from the Developer Console, be careful to keep this hidden.
channel: The name of the Twitch channel you want the bot to join (your main account).
The rest are details necessary for the use of some commands, and not necessary if you don't need these.

Example config.json:

    {
    "username": "my_bot_account_name",
    "client_id": "ab1cdef23ghijk45lmnopq67rstu",
    "client_secret": "abc1d2efg45hijk67lmnopqrstuvwx"
    "channel": "the_streamer_channel_name"
    "fyrewire_key": "52a1e702ab7362b7e5bc8c3dfe6f8e8f",
    "val_user": "<Streamer_valorant_username>",
    "val_tag": "<Streamer_valorant_tag>",
    "val_region": "<Streamer_valorant_region>",
    "discord_link": "<Streamer_discord_link>",
    "tiktok_link": "<Streamer_tiktok_link>",
    "instagram_link": "<Streamer_instagram_link>",
    "twitter_link": "<Streamer_twitter_link>",
    "youtube_link": "<Streamer_youtube_link>",
    "donate_link": "<Streamer_donations_link>",
    "nickname": "<Streamer_nickname>"
    }

Ensuring everything in your config.json is lower case.

## 3. Running the Bot

Make sure you have Go installed on your system (download here).
Open your terminal, navigate to the directory containing the Go files, and run the application:

    go run .

During launch, your default web browser will open a Twitch authorization page.
Log in to the account you want the bot to use (your username from the config) and click Authorize.
You will be redirected to a page that says "Authentication successful!". You can close the browser tab.
The bot is now authenticated and will connect to the specified Twitch channel.

## Adding New Commands

Adding commands is simple:
Open commands.go.
Create a new function with the signature 

    func(user User, args []string) (string, error)

In the RegisterCommands function, register your new command with handler.Register("yourcommand", yourCommandFunction).