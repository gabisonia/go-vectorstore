module apphost

go 1.26.0

require apphost/modules/aspire v0.0.0

replace apphost/modules/aspire => ./.aspire/modules
