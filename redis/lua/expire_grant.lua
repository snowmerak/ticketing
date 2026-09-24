local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
local raw = redis.call('HGET', KEYS[3], ARGV[4])
if not raw then
  redis.call('ZREM', KEYS[5], ARGV[4])
  return {'MISSING'}
end
local grant = cjson.decode(raw)
if grant.epoch ~= ARGV[1] or tostring(grant.seq) ~= ARGV[2] then
  return {'MISMATCH'}
end
if tonumber(grant.expires_at_ms) > now_ms then
  return {'NOT_EXPIRED'}
end
redis.call('SETBIT', KEYS[2], tonumber(ARGV[3]), 0)
for index = 6, #KEYS do
  redis.call('SETBIT', KEYS[index], tonumber(ARGV[3]), 0)
end
redis.call('HDEL', KEYS[3], ARGV[4])
redis.call('HDEL', KEYS[4], ARGV[1] .. ':' .. ARGV[2])
redis.call('ZREM', KEYS[5], ARGV[4])
local reserved = tonumber(redis.call('HGET', KEYS[1], 'reserved_count') or '0')
if reserved > 0 then redis.call('HINCRBY', KEYS[1], 'reserved_count', -1) end
return {'EXPIRED'}
