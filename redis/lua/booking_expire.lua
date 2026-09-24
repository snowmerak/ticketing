local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
local raw = redis.call('HGET', KEYS[2], ARGV[1])
if not raw then
  redis.call('ZREM', KEYS[3], ARGV[1])
  return {'MISSING'}
end
local booking = cjson.decode(raw)
if tonumber(booking.idle_expires_at_ms) > now_ms and tonumber(booking.absolute_expires_at_ms) > now_ms then
  return {'NOT_EXPIRED'}
end
redis.call('HDEL', KEYS[2], ARGV[1])
redis.call('ZREM', KEYS[3], ARGV[1])
local active = tonumber(redis.call('HGET', KEYS[1], 'active_count') or '0')
if active > 0 then redis.call('HINCRBY', KEYS[1], 'active_count', -1) end
return {'EXPIRED'}
