local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
local seq_key = ARGV[1] .. ':' .. ARGV[2]
local existing_booking = redis.call('HGET', KEYS[8], seq_key)
if existing_booking then
  local booking = redis.call('HGET', KEYS[9], existing_booking)
  if booking then
    local decoded_booking = cjson.decode(booking)
    return {'OK', existing_booking, tostring(decoded_booking.idle_expires_at_ms), tostring(decoded_booking.absolute_expires_at_ms), 'REPLAY'}
  end
  return {'BOOKING_ENDED', existing_booking}
end
local grant_raw = redis.call('HGET', KEYS[5], ARGV[4])
if not grant_raw then
  return {'GRANT_MISSING'}
end
local grant = cjson.decode(grant_raw)
if grant.epoch ~= ARGV[1] or tostring(grant.seq) ~= ARGV[2]
    or redis.call('HGET', KEYS[6], seq_key) ~= ARGV[4] then
  return {'GRANT_MISMATCH'}
end
if tonumber(grant.expires_at_ms) <= now_ms then
  return {'GRANT_EXPIRED'}
end
if redis.call('GETBIT', KEYS[4], tonumber(ARGV[3])) == 1 then
  return {'SPENT'}
end
local absolute_expiry = now_ms + tonumber(ARGV[8])
local idle_expiry = math.min(now_ms + tonumber(ARGV[7]), absolute_expiry)
local booking = cjson.encode({subject_id=ARGV[5], idle_expires_at_ms=idle_expiry, absolute_expires_at_ms=absolute_expiry})
redis.call('SETBIT', KEYS[3], tonumber(ARGV[3]), 0)
redis.call('SETBIT', KEYS[4], tonumber(ARGV[3]), 1)
redis.call('HDEL', KEYS[5], ARGV[4])
redis.call('HDEL', KEYS[6], seq_key)
redis.call('ZREM', KEYS[7], ARGV[4])
redis.call('HSET', KEYS[8], seq_key, ARGV[6])
redis.call('HSET', KEYS[9], ARGV[6], booking)
redis.call('ZADD', KEYS[10], idle_expiry, ARGV[6])
local reserved = tonumber(redis.call('HGET', KEYS[1], 'reserved_count') or '0')
if reserved > 0 then redis.call('HINCRBY', KEYS[1], 'reserved_count', -1) end
redis.call('HINCRBY', KEYS[1], 'active_count', 1)
return {'OK', ARGV[6], tostring(idle_expiry), tostring(absolute_expiry), 'NEW'}
