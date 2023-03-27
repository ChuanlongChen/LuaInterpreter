-- 测试编译器
-- 表的定义、循环遍历
t = {a = 1, b = 2, c = 3}
for k, v in pairs(t) do
    print(k, v)
end

-- 函数的定义、调用
function add(x, y)
    return x + y
end

local x,y = 123, 456
local sum = add(x, y) 
print(sum)
