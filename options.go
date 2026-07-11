package awsapimcproxy

// Options holds the command-line options.
type Options struct {
	Config string `kong:"required,short='c',env='AWSAPIMCPROXY_CONFIG',help='Config file path.'"`
}
