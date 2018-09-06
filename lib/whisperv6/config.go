package whisperv6

type Config struct {
	MaxMessageSize                        uint32  `toml:",omitempty"`
	MinimumAcceptedPOW                    float64 `toml:",omitempty"`
	RestrictConnectionBetweenLightClients bool    `toml:",omitempty"`
}

var DefaultConfig = Config{
	MaxMessageSize:                        DefaultMaxMessageSize,
	MinimumAcceptedPOW:                    DefaultMinimumPoW,
	RestrictConnectionBetweenLightClients: true,
}
