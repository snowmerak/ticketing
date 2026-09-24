local raw = redis.call('HGET', KEYS[2], ARGV[1])
if not raw then return {'MISSING'} end
local booking = cjson.decode(raw)
if booking.subject_id ~= ARGV[2] then return {'OWNER_MISMATCH'} end
redis.call('HDEL', KEYS[2], ARGV[1])
redis.call('ZREM', KEYS[3], ARGV[1])
local active = tonumber(redis.call('HGET', KEYS[1], 'active_count') or '0')
if active > 0 then redis.call('HINCRBY', KEYS[1], 'active_count', -1) end
return {'LEFT'}
