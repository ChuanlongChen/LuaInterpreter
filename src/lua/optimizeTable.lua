start_t = clock() -- 开始时间（纳秒）

x = {}
i = 0  -- 开始下标
gap = 2  -- 循环步长，测试样例A为1，测试样例B为2
max_index = 2e6  --结束下标
while( i < max_index) do
    x[i] = i
    i = i + gap
end

i = 0
while( i < max_index) do 
    x[i] = x[i] + i
    i = i + gap
end

end_t = clock()   -- 结束时间（纳秒）
print( (end_t - start_t) /1e9) -- 运行耗时（秒）


