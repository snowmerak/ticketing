local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
local owner = redis.call('HGET', KEYS[1], 'writer_owner') or ''
local until_ms = tonumber(redis.call('HGET', KEYS[1], 'writer_until_ms') or '0')
local fence = tonumber(redis.call('HGET', KEYS[1], 'writer_fence') or '0')
if owner ~= '' and owner ~= ARGV[1] and until_ms > now_ms then
  return {'BUSY', owner, tostring(until_ms), tostring(fence)}
end
if owner ~= ARGV[1] or until_ms <= now_ms then
  fence = fence + 1
end
until_ms = now_ms + tonumber(ARGV[2])
redis.call('HSET', KEYS[1], 'writer_owner', ARGV[1], 'writer_until_ms', until_ms, 'writer_fence', fence)
return {'OK', tostring(fence), tostring(until_ms), tostring(now_ms)}
