package api

import "linkstar/api/stun_api"

type Api struct {
	StunApi stun_api.StunApi
}

var App = new(Api)
