-- 表和迭代器
t = {a = 1, b = 2, c = 3}
t['b'] = 666
for k, v in pairs(t) do
  print(k, v)
end

t = {"a"}
t[2],t[3] = "b", "c"
for k, v in ipairs(t) do
  print(k, v)
end
