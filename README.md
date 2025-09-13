# TG Reply Bot

Bot for auto reply to private messages ()

## Run

1. Get APP_ID and APP_HASH from [my.telegram.org](https://my.telegram.org/apps)

2. Add .env file:
	```text
	TG_PHONE=+7999999999
	APP_ID=
	APP_HASH=
	REPLY_MSG="Hi, i'm on vacation, please contact me later"
	```

3. Run binary: `go run cmd/main.go` or build in Docker: `docker build -f zarf/Docker/Dockerfile -t reply_bot`

	*P.S: Better to set up bot as a binary and then pack it into docker image.*