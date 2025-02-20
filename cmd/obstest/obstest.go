package main

import (
	"github.com/emmaly/musicstate/obs/config"
)

func main() {
	c, err := config.GetLocalConfig()
	if err != nil {
		panic(err)
	}

	println("Port:", c.Port)
	println("Password:", c.Password)
}
