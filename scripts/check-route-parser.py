import pathlib,subprocess,sys,os
sys.stdout.reconfigure(encoding='utf-8')
root=pathlib.Path(__file__).resolve().parents[1]
source=(root/'scripts/kokoro-bench.sh').read_text(encoding='utf-8')
marker="brief=$(printf '%s' \"$out\" | python3 -c '"
start=source.index(marker)+len(marker)
parser=source[start:source.index("\n' \"$nm\" 2>/dev/null)",start)]
cases=[
('missing ASN on CN2 hop','电信', '1|10.52.3.1||0.5||RFC1918|||||\n10|59.43.80.145||127||中国|上海|||chinatelecom.cn\n14|219.141.160.38||151|4847|中国|北京|||电信\n', '电信 CN2 GIA（AS4809）'),
('Softbank transit, repeated probes','电信','1|192.0.2.1||1|205548|日本|东京|||origin\n4|192.0.2.2||2|17676|日本|东京|||Softbank\n7|192.0.2.3||30|17676|中国|上海|||Softbank\n9|202.97.1.1||46|4134|中国|上海|||电信\n9|202.97.1.1||47|4134|中国|上海|||电信\n', 'Softbank → 电信 163（AS4134）'),
('IPv6 CMIN2','移动','3|2001:db8::3||5|58807|中国|上海|||移动\n4|2001:db8::4||8|9808|中国|上海|||移动\n','移动 CMIN2（AS58807）'),
('timeout never claims a backbone','电信','1|*||||||\n2|*||||||\n','未收到有效路由响应（目标过滤或探测超时）'),
('legacy terminal fallback','联通','1 192.0.2.1 AS2914 日本 东京 NTT 2.0ms\n8 219.158.1.1 AS4837 中国 上海 联通 30ms\n9 203.0.113.2 AS4808 中国 北京 35ms\n','NTT → 联通 4837（AS4837）'),
]
for name,carrier,raw,want in cases:
    result=subprocess.run([sys.executable,'-c',parser,carrier],input=raw,text=True,encoding='utf-8',capture_output=True,check=True,env=dict(os.environ,PYTHONIOENCODING='utf-8'))
    got=result.stdout.strip()
    assert got==want,(name,got,want)
    print('PASS',name)
