package rediscripts

import _ "embed"

//go:embed entry.lua
var Entry string

//go:embed heartbeat.lua
var Heartbeat string

//go:embed acquire_lease.lua
var AcquireLease string

//go:embed claim.lua
var Claim string

//go:embed redeem.lua
var Redeem string

//go:embed expire_grant.lua
var ExpireGrant string

//go:embed booking_heartbeat.lua
var BookingHeartbeat string

//go:embed booking_leave.lua
var BookingLeave string

//go:embed booking_expire.lua
var BookingExpire string

//go:embed close_epoch.lua
var CloseEpoch string
