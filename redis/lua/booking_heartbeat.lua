local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
local raw = redis.call('HGET', KEYS[1], ARGV[1])
if not raw then return {'MISSING'} end
local booking = cjson.decode(raw)
if booking.subject_id ~= ARGV[2] then return {'OWNER_MISMATCH'} end
if tonumber(booking.idle_expires_at_ms) <= now_ms or tonumber(booking.absolute_expires_at_ms) <= now_ms then
  return {'EXPIRED'}
end
local idle_expiry = math.min(now_ms + tonumber(ARGV[3]), tonumber(booking.absolute_expires_at_ms))
booking.idle_expires_at_ms = idle_expiry
redis.call('HSET', KEYS[1], ARGV[1], cjson.encode(booking))
redis.call('ZADD', KEYS[2], idle_expiry, ARGV[1])
return {'OK', tostring(idle_expiry), tostring(booking.absolute_expires_at_ms), tostring(now_ms)}
