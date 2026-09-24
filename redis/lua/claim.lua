local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
if redis.call('HGET', KEYS[1], 'mode') ~= 'QUEUE' or redis.call('HGET', KEYS[1], 'pause') == '1' then
  return {'PAUSED'}
end
if redis.call('HGET', KEYS[1], 'writer_owner') ~= ARGV[5]
    or tonumber(redis.call('HGET', KEYS[1], 'writer_fence') or '-1') ~= tonumber(ARGV[4])
    or tonumber(redis.call('HGET', KEYS[1], 'writer_until_ms') or '0') <= now_ms then
  return {'STALE_WRITER'}
end
if redis.call('HGET', KEYS[2], 'state') ~= 'OPEN' then
  return {'CLOSED'}
end
local ready = false
for index = 8, #KEYS do
  if redis.call('GETBIT', KEYS[index], tonumber(ARGV[3])) == 1 then
    ready = true
    break
  end
end
if not ready then
  return {'NOT_READY'}
end
if redis.call('GETBIT', KEYS[3], tonumber(ARGV[3])) == 1 then
  return {'SPENT'}
end
if redis.call('GETBIT', KEYS[4], tonumber(ARGV[3])) == 1 then
  return {'RESERVED'}
end
local capacity = tonumber(redis.call('HGET', KEYS[1], 'capacity') or '0')
local active = tonumber(redis.call('HGET', KEYS[1], 'active_count') or '0')
local reserved = tonumber(redis.call('HGET', KEYS[1], 'reserved_count') or '0')
if active + reserved >= capacity then
  return {'NO_CAPACITY'}
end
local rate = tonumber(redis.call('HGET', KEYS[1], 'admission_rate') or '0')
local burst = tonumber(redis.call('HGET', KEYS[1], 'admission_burst') or '0')
local tokens = tonumber(redis.call('HGET', KEYS[1], 'rate_tokens') or tostring(burst))
local token_at = tonumber(redis.call('HGET', KEYS[1], 'rate_token_at_ms') or tostring(now_ms))
if now_ms > token_at then
  tokens = math.min(burst, tokens + ((now_ms - token_at) / 1000.0) * rate)
  token_at = now_ms
end
if tokens < 1 then
  redis.call('HSET', KEYS[1], 'rate_tokens', tokens, 'rate_token_at_ms', token_at)
  return {'RATE_LIMITED'}
end
local expires_at = now_ms + tonumber(ARGV[7])
local value = cjson.encode({epoch=ARGV[1], seq=ARGV[2], expires_at_ms=expires_at})
redis.call('SETBIT', KEYS[4], tonumber(ARGV[3]), 1)
redis.call('HSET', KEYS[5], ARGV[6], value)
redis.call('HSET', KEYS[6], ARGV[1] .. ':' .. ARGV[2], ARGV[6])
redis.call('ZADD', KEYS[7], expires_at, ARGV[6])
redis.call('HINCRBY', KEYS[1], 'reserved_count', 1)
redis.call('HSET', KEYS[1], 'rate_tokens', tokens - 1, 'rate_token_at_ms', token_at)
return {'OK', tostring(expires_at), tostring(now_ms)}
