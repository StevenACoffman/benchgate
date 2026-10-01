// A throwaway module the gotest suite runs the real toolchain against. It lives
// under testdata so the root module's ./... never reaches it.
module benchgate.test/mod

go 1.27.1
