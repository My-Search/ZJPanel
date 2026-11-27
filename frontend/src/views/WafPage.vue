<template>
  <a-spin :spinning="loading">
    <div class="waf-page">
      <!-- 顶部告警：流量风险优先；风险正常时提示防护链路是否有环节未生效 -->
      <a-alert
        v-if="snap.level === 'critical'"
        type="error" show-icon banner class="banner"
        :message="`当前站点处于高风险状态：健康分 ${snap.health}/100，近 60 秒有 ${snap.active} 个严重事件`"
        description="先看「安全态势」定位来源，再决定封 IP 还是封地区；封禁前建议先点一次预演。"
      />
      <a-alert
        v-else-if="snap.level === 'warn'"
        type="warning" show-icon banner class="banner"
        :message="`检测到异常流量：健康分 ${snap.health}/100`"
      />
      <a-alert
        v-else-if="blockReason"
        type="warning" show-icon banner class="banner"
        :message="`防护未完全生效：${blockReason}`"
        description="命中规则仍会记录攻击日志，但不会向 nginx 下发封禁；检查「策略与规则」中的检测与下发开关。"
      >
        <template #action>
          <a-button size="small" @click="tab = 'policy'">调整策略</a-button>
        </template>
      </a-alert>

      <!-- 指标条：态势与规则数据合成同一行，两个来源共用一套卡片规格 -->
      <a-row :gutter="12">
        <a-col :xs="12" :md="6">
          <a-card size="small" class="stat">
            <div class="stat-label">健康分</div>
            <div class="stat-value" :style="{ color: healthColor }">{{ snap.health ?? 100 }}<span class="unit">/100</span></div>
            <div class="stat-sub">{{ levelText }}</div>
          </a-card>
        </a-col>
        <a-col :xs="12" :md="6">
          <a-card size="small" class="stat">
            <div class="stat-label">实时 RPS</div>
            <div class="stat-value">{{ fmtNum(snap.rps) }}<span class="unit">req/s</span></div>
            <div class="stat-sub">基线 {{ fmtNum(snap.baselineRps) }} req/s</div>
          </a-card>
        </a-col>
        <a-col :xs="12" :md="6">
          <a-card size="small" class="stat">
            <div class="stat-label">4xx / 5xx（60s）</div>
            <div class="stat-value">
              {{ fmtNum(snap.status?.['4xx']) }}<span class="unit">/</span><span :style="{ color: (snap.status?.['5xx'] || 0) > 0 ? '#ef4444' : undefined }">{{ fmtNum(snap.status?.['5xx']) }}</span>
            </div>
            <div class="stat-sub">错误率 {{ snap.errRate ?? 0 }}%</div>
          </a-card>
        </a-col>
        <a-col :xs="12" :md="6">
          <a-card size="small" class="stat">
            <div class="stat-label">WAF 命中（1 小时）</div>
            <div class="stat-value">{{ fmtNum(stats.lastHour) }}<span class="unit">次</span></div>
            <div class="stat-sub">规则 {{ enabledRules }}/{{ cfg.rules.length }} · 处置 {{ hitModeText }}</div>
          </a-card>
        </a-col>
      </a-row>

      <a-tabs v-model:activeKey="tab" class="waf-tabs" @change="onTabChange">
        <!-- ============ 安全态势：实时监控与封禁处置 ============ -->
        <a-tab-pane key="overview" tab="安全态势">
          <div class="tab-body">
            <a-row :gutter="12">
              <a-col :xs="24" :lg="16">
                <a-card size="small" title="实时请求速率（近 2 分钟）">
                  <div ref="rpsBox" class="chart"></div>
                </a-card>
              </a-col>
              <a-col :xs="24" :lg="8">
                <a-card size="small" title="内核层观测" style="height: 100%">
                  <a-descriptions :column="1" size="small">
                    <a-descriptions-item label="ESTABLISHED">{{ fmtNum(l4.conn) }}</a-descriptions-item>
                    <a-descriptions-item label="SYN_RECV">{{ fmtNum(l4.synRecv) }}</a-descriptions-item>
                    <a-descriptions-item label="TIME_WAIT">{{ fmtNum(l4.timeWait) }}</a-descriptions-item>
                    <a-descriptions-item label="入向带宽">{{ fmtBps(l4.rxBps) }}</a-descriptions-item>
                    <a-descriptions-item label="accept 队列溢出">{{ fmtNum(l4.overflows) }}</a-descriptions-item>
                  </a-descriptions>
                  <div class="hint">日志只能看见 HTTP 攻击；SYN Flood / 连接打满这类攻击靠这一层兜底。</div>
                </a-card>
              </a-col>
            </a-row>

            <a-row :gutter="12">
              <a-col :xs="24" :lg="14">
                <a-card size="small" title="攻击事件（实时）" :body-style="{ padding: '0 8px 8px' }">
                  <a-table
                    :data-source="snap.events || []" :columns="eventCols" row-key="id"
                    size="small" :pagination="false" :scroll="{ y: 260 }"
                    :locale="{ emptyText: '暂无事件，一切正常' }"
                  >
                    <template #bodyCell="{ column, record }">
                      <template v-if="column.key === 'time'">{{ fmtClock(record.time) }}</template>
                      <template v-else-if="column.key === 'severity'">
                        <a-tag :color="record.severity === 'critical' ? 'red' : record.severity === 'warn' ? 'orange' : 'blue'">
                          {{ record.severity === 'critical' ? '严重' : record.severity === 'warn' ? '警告' : '提示' }}
                        </a-tag>
                      </template>
                      <template v-else-if="column.key === 'kind'">{{ record.kind }}</template>
                      <template v-else-if="column.key === 'source'">
                        <div v-if="record.site" class="mono" :title="record.site">{{ record.site }}</div>
                        <div v-if="record.ip">
                          <span class="mono">{{ record.ip }}</span>
                          <a-tag v-if="record.country" class="cc">{{ record.country }}</a-tag>
                        </div>
                        <span v-if="!record.site && !record.ip && !record.country" class="dim">-</span>
                      </template>
                      <template v-else-if="column.key === 'detail'">
                        <span class="detail">{{ record.detail }}</span>
                        <a v-if="record.ip" class="mini-act" @click="askBlockIp(record.ip, record.kind)">封禁</a>
                      </template>
                    </template>
                  </a-table>
                </a-card>
              </a-col>
              <a-col :xs="24" :lg="10">
                <a-card size="small" title="来源地区 Top（60s）" :body-style="{ padding: '0 8px 8px' }">
                  <div v-if="!snap.geoLoaded" class="hint" style="padding: 2px 4px 6px">
                    GeoIP 库未加载：地区统计为空、无法按国家封禁，可在「请求地图」页上传 GeoLite2-Country.mmdb
                  </div>
                  <a-table
                    :data-source="snap.topCountries || []" :columns="countryCols" row-key="name"
                    size="small" :pagination="false" :scroll="{ y: 260 }"
                    :locale="{ emptyText: '暂无数据' }"
                  >
                    <template #bodyCell="{ column, record }">
                      <template v-if="column.key === 'name'">
                        <span class="mono">{{ record.name }}</span>
                      </template>
                      <template v-else-if="column.key === 'count'">{{ fmtNum(record.count) }} <span class="pct">{{ record.pct }}%</span></template>
                      <template v-else-if="column.key === 'act'">
                        <a-button size="small" danger type="text" :disabled="!snap.geoLoaded" @click="askBlockCountry(record.name)">封禁</a-button>
                      </template>
                    </template>
                  </a-table>
                </a-card>
              </a-col>
            </a-row>

            <a-row :gutter="12">
              <a-col :xs="24" :lg="12">
                <a-card size="small" title="手动处置">
                  <a-space direction="vertical" style="width: 100%">
                    <a-input-group compact>
                      <a-input v-model:value="ipInput" placeholder="IP 或 CIDR，如 1.2.3.4 / 1.2.3.0/24" style="width: calc(100% - 180px)" />
                      <a-select v-model:value="ipMinutes" style="width: 90px">
                        <a-select-option :value="60">60 分钟</a-select-option>
                        <a-select-option :value="360">6 小时</a-select-option>
                        <a-select-option :value="1440">1 天</a-select-option>
                        <a-select-option :value="0">永久</a-select-option>
                      </a-select>
                      <a-button type="primary" danger @click="askBlockIp(ipInput, '手动')" style="width: 90px">封禁</a-button>
                    </a-input-group>
                    <div class="hint">封禁前会自动跑 5 分钟预演，展示该来源的正常请求占比，避免误伤。</div>
                  </a-space>
                </a-card>
              </a-col>
              <a-col :xs="24" :lg="12">
                <a-card size="small" title="当前封禁">
                  <div v-if="!blockedRows.length" class="hint">暂无封禁项</div>
                  <a-table
                    v-else :data-source="blockedRows" :columns="blockCols" row-key="key"
                    size="small" :pagination="false" :scroll="{ y: 160 }"
                  >
                    <template #bodyCell="{ column, record }">
                      <template v-if="column.key === 'kind'">
                        <a-tag :color="record.kind === 'country' ? 'blue' : 'default'">{{ record.kind === 'country' ? '地区' : 'IP' }}</a-tag>
                      </template>
                      <template v-else-if="column.key === 'until'">{{ record.until ? fmtTime(record.until) : '永久' }}</template>
                      <template v-else-if="column.key === 'act'">
                        <a-button size="small" type="link" @click="unblock(record)">解封</a-button>
                      </template>
                    </template>
                  </a-table>
                </a-card>
              </a-col>
            </a-row>
          </div>
        </a-tab-pane>

        <!-- ============ 策略与规则：检测（WAF）→ 自动封禁 → 下发（安全中心） ============ -->
        <a-tab-pane key="policy" tab="策略与规则">
          <div class="tab-body">
            <div class="pipeline">
              <span class="pipe-title">生效链路</span>
              <template v-for="(node, i) in pipeline" :key="node.label">
                <span v-if="i > 0" class="pipe-arrow">→</span>
                <span class="pipe-node" :class="node.state"><i class="pipe-dot"></i>{{ node.label }} · {{ node.text }}</span>
              </template>
              <span class="pipe-hint">任一环节「观察 / 关闭」时，命中只记录不拦截。</span>
            </div>

            <a-card size="small" title="检测策略（WAF 规则引擎）" :body-style="{ paddingTop: '12px' }">
              <template #extra>
                <a-tag :color="cfg.settings.enabled ? 'green' : 'default'">{{ cfg.settings.enabled ? '检测中' : '已关闭' }}</a-tag>
              </template>
              <a-form layout="vertical" :model="form">
                <a-row :gutter="16">
                  <a-col :xs="12" :md="4">
                    <a-form-item label="WAF 总开关">
                      <a-switch v-model:checked="form.enabled" checked-children="开" un-checked-children="关" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="5">
                    <a-form-item label="命中处置">
                      <a-radio-group v-model:value="form.blockMode">
                        <a-radio-button value="observe">观察</a-radio-button>
                        <a-radio-button value="block">拦截</a-radio-button>
                      </a-radio-group>
                      <div class="hint">观察只记录不封禁</div>
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="4">
                    <a-form-item label="自动封禁">
                      <a-switch v-model:checked="form.autoBan" checked-children="开" un-checked-children="关" />
                      <div class="hint">仅拦截模式生效</div>
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="4">
                    <a-form-item label="累犯清零（小时）">
                      <a-input-number v-model:value="form.banResetHours" :min="1" :max="720" style="width: 100%" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="7">
                    <a-form-item label="封禁阶梯（分钟）">
                      <a-input v-model:value="ladderText" placeholder="10,60,1440" />
                      <div class="hint">第 1/2/3… 次命中依次取阶梯，超出取最后一级</div>
                    </a-form-item>
                  </a-col>
                </a-row>
                <a-row :gutter="16">
                  <a-col :xs="24" :md="18">
                    <a-form-item label="规则豁免地址（每行一个 IP / CIDR）">
                      <a-textarea v-model:value="exemptText" :rows="3" placeholder="127.0.0.1&#10;::1&#10;10.0.0.0/8" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="6">
                    <a-form-item label=" ">
                      <a-space direction="vertical">
                        <a-button type="primary" :loading="savingWaf" @click="saveSettings">保存检测策略</a-button>
                        <div class="hint">豁免地址命中规则也不判定、不记录；内置 127.0.0.1 / ::1。</div>
                      </a-space>
                    </a-form-item>
                  </a-col>
                </a-row>
              </a-form>
            </a-card>

            <a-card size="small" title="封禁下发（安全中心）" :body-style="{ paddingTop: '12px' }">
              <template #extra>
                <a-tag :color="!pol.enabled ? 'default' : pol.observe ? 'orange' : 'red'">{{ enforceText }}</a-tag>
              </template>
              <a-form layout="vertical">
                <a-row :gutter="16">
                  <a-col :xs="12" :md="4">
                    <a-form-item label="启用封禁下发">
                      <a-switch v-model:checked="pol.enabled" />
                      <div class="hint">关闭后只观测，不改 nginx</div>
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="5">
                    <a-form-item label="观察模式">
                      <a-switch v-model:checked="pol.observe" />
                      <div class="hint">判定照常，下发只记录</div>
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="5">
                    <a-form-item label="扫描/爆破自动封禁">
                      <a-switch v-model:checked="pol.autoBan" />
                      <div class="hint">超阈值自动封 60 分钟</div>
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="4">
                    <a-form-item label="CC 限速">
                      <a-switch v-model:checked="pol.ccEnabled" />
                      <div class="hint">nginx limit_req</div>
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="3">
                    <a-form-item label="速率 r/s">
                      <a-input-number v-model:value="pol.ccRate" :min="1" :max="2000" style="width: 100%" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="3">
                    <a-form-item label="突发 burst">
                      <a-input-number v-model:value="pol.ccBurst" :min="0" :max="2000" style="width: 100%" />
                    </a-form-item>
                  </a-col>
                </a-row>
                <a-row :gutter="16">
                  <a-col :xs="24" :md="18">
                    <a-form-item label="可信来源白名单（每行一个 IP / CIDR）">
                      <a-textarea v-model:value="whitelistText" :rows="3" placeholder="127.0.0.1&#10;192.168.0.0/16" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="6">
                    <a-form-item label=" ">
                      <a-space direction="vertical">
                        <a-button type="primary" :loading="savingPolicy" @click="savePolicy">保存并下发</a-button>
                        <div class="hint">WAF 自动封禁、手动/地区封禁共用这份策略；保存写入 nginx 并 reload，失败自动回滚。</div>
                      </a-space>
                    </a-form-item>
                  </a-col>
                </a-row>
              </a-form>
            </a-card>

            <a-card size="small" :title="`规则列表（${cfg.rules.length}）`" :body-style="{ padding: '0 8px 8px' }">
              <template #extra>
                <a-space>
                  <a-button size="small" @click="openTest">规则测试</a-button>
                  <a-button size="small" type="primary" @click="openAdd">新增自定义规则</a-button>
                </a-space>
              </template>
              <a-table
                :data-source="cfg.rules" :columns="ruleCols" row-key="id"
                size="small" :pagination="false" :scroll="{ y: 420 }"
              >
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'name'">
                    <div class="rule-name">{{ record.name }}</div>
                    <div class="rule-desc">{{ record.builtin ? record.desc : record.pattern }}</div>
                  </template>
                  <template v-else-if="column.key === 'category'">
                    <a-tag :color="catColor(record.category)">{{ catText(record.category) }}</a-tag>
                  </template>
                  <template v-else-if="column.key === 'severity'">
                    <a-tag :color="record.severity === 'high' ? 'red' : record.severity === 'medium' ? 'orange' : 'default'">
                      {{ record.severity === 'high' ? '高' : record.severity === 'medium' ? '中' : '低' }}
                    </a-tag>
                  </template>
                  <template v-else-if="column.key === 'action'">
                    <a-select
                      :value="record.action" size="small" style="width: 92px"
                      :options="[{ value: 'observe', label: '观察' }, { value: 'block', label: '拦截' }]"
                      @change="(v: any) => updateRule(record, record.enabled, v)"
                    />
                  </template>
                  <template v-else-if="column.key === 'enabled'">
                    <a-switch size="small" :checked="record.enabled" @change="(v: any) => updateRule(record, !!v, record.action)" />
                  </template>
                  <template v-else-if="column.key === 'act'">
                    <a-button v-if="!record.builtin" size="small" danger type="text" @click="removeRule(record)">删除</a-button>
                    <span v-else class="dim">内置</span>
                  </template>
                </template>
              </a-table>
            </a-card>
          </div>
        </a-tab-pane>

        <!-- ============ 攻击日志：规则命中证据与快速封禁 ============ -->
        <a-tab-pane key="hits" :tab="`攻击日志（${hits.length}）`">
          <div class="tab-body">
            <a-card size="small" :body-style="{ padding: '8px 8px 8px' }">
              <div class="hits-bar">
                <a-select v-model:value="filter.category" :options="catOptions" style="width: 150px" allow-clear placeholder="全部类别" @change="loadHits(true)" />
                <a-input v-model:value="filter.ip" placeholder="按 IP 过滤" style="width: 180px" allow-clear @press-enter="loadHits(true)" />
                <a-button @click="loadHits(true)">查询</a-button>
                <span class="dim">命中证据含解码后的攻击载荷片段，可直接用于回溯；日志落盘 data/waf/hits.jsonl</span>
              </div>
              <a-table
                :data-source="hits" :columns="hitCols" row-key="_k"
                size="small" :loading="hitsLoading"
                :pagination="{ pageSize: 20, size: 'small' }" :scroll="{ y: 460 }"
                :locale="{ emptyText: '暂无命中，一切正常' }"
              >
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'time'">{{ fmtTime(record.time) }}</template>
                  <template v-else-if="column.key === 'rule'">
                    <div>{{ record.ruleName }}</div>
                    <a-tag :color="catColor(record.category)">{{ catText(record.category) }}</a-tag>
                    <a-tag v-if="record.count > 1" color="purple">×{{ record.count }}</a-tag>
                  </template>
                  <template v-else-if="column.key === 'source'">
                    <div class="mono">{{ record.ip }}</div>
                    <div class="dim">{{ record.site }}{{ record.country ? ' · ' + record.country : '' }}</div>
                  </template>
                  <template v-else-if="column.key === 'path'">
                    <div class="mono ellipsis" :title="record.path">{{ record.method }} {{ record.path }}</div>
                    <div v-if="record.snippet" class="snippet" :title="record.snippet">{{ record.snippet }}</div>
                  </template>
                  <template v-else-if="column.key === 'action'">
                    <a-tag :color="record.action === 'block' ? 'red' : 'orange'">{{ record.action === 'block' ? '拦截' : '观察' }}</a-tag>
                    <a v-if="record.ip" class="mini-act" @click="askBlock(record.ip)">封禁</a>
                  </template>
                </template>
              </a-table>
            </a-card>
          </div>
        </a-tab-pane>
      </a-tabs>

      <a-modal v-model:open="testOpen" title="规则测试" :footer="null" width="760px">
        <div class="hint" style="margin-bottom: 12px">构造一条请求看会命中哪些规则，不会封禁；编码与 access log 保持一致。</div>
        <a-form layout="vertical">
          <a-row :gutter="12">
            <a-col :xs="24" :md="3">
              <a-form-item label="方法">
                <a-input v-model:value="test.method" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12">
              <a-form-item label="路径 + 查询串">
                <a-input v-model:value="test.path" placeholder="/search?q=1%27%20OR%201%3D1--" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="9">
              <a-form-item label="User-Agent">
                <a-input v-model:value="test.ua" placeholder="sqlmap/1.7.2" />
              </a-form-item>
            </a-col>
          </a-row>
          <a-space wrap>
            <a-button type="primary" :loading="testing" @click="runTest">检测</a-button>
            <a-button @click="fillSample('sqli')">SQLi 样例</a-button>
            <a-button @click="fillSample('xss')">XSS 样例</a-button>
            <a-button @click="fillSample('lfi')">穿越样例</a-button>
            <a-button @click="fillSample('scanner')">扫描器样例</a-button>
          </a-space>
        </a-form>
        <a-alert
          v-if="testDone"
          class="test-result"
          :type="testHits.length ? 'error' : 'success'"
          show-icon
          :message="testHits.length ? `命中 ${testHits.length} 条规则` : '未命中任何规则'"
        >
          <div v-for="h in testHits" :key="h.ruleId" class="test-hit">
            <b>{{ h.ruleName }}</b>
            <a-tag :color="h.action === 'block' ? 'red' : 'orange'">{{ h.action === 'block' ? '拦截' : '观察' }}</a-tag>
            <span class="mono">{{ h.snippet }}</span>
          </div>
        </a-alert>
      </a-modal>

      <a-modal v-model:open="addOpen" title="新增自定义规则" :confirm-loading="adding" @ok="submitRule">
        <a-form layout="vertical">
          <a-form-item label="规则名">
            <a-input v-model:value="draft.name" placeholder="例如：拦截内部调试接口外网访问" :maxlength="40" />
          </a-form-item>
          <a-form-item label="正则（默认忽略大小写，作用于解码后的内容）">
            <a-input v-model:value="draft.pattern" placeholder="例如：/internal/debug\?token=" />
          </a-form-item>
          <a-form-item label="作用范围">
            <a-checkbox-group v-model:value="draft.scope" :options="scopeOptions" />
          </a-form-item>
          <a-row :gutter="12">
            <a-col :span="12">
              <a-form-item label="动作">
                <a-select v-model:value="draft.action" :options="[{ value: 'observe', label: '观察' }, { value: 'block', label: '拦截' }]" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="严重级别">
                <a-select v-model:value="draft.severity" :options="[{ value: 'high', label: '高' }, { value: 'medium', label: '中' }, { value: 'low', label: '低' }]" />
              </a-form-item>
            </a-col>
          </a-row>
          <div class="hint">保存前会用正则引擎编译校验，坏正则直接拒绝，不会写进配置。</div>
        </a-form>
      </a-modal>
    </div>
  </a-spin>
</template>

<script setup lang="ts">
import { computed, nextTick, onActivated, onDeactivated, onMounted, onUnmounted, reactive, ref } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { del, fmtBytes, get, getQuiet, post, put, wsTopic } from '../api'
import { usePolling } from '../composables/usePolling'

interface Rule { id: string; name: string; category: string; severity: string; action: string; scope?: string[]; pattern?: string; enabled: boolean; builtin: boolean; desc?: string }
interface Settings { enabled: boolean; blockMode: string; autoBan: boolean; banLadder: number[]; banResetHours: number; exempt: string[] }
interface Hit { time: string; ruleId: string; ruleName: string; category: string; severity: string; action: string; site?: string; ip: string; country?: string; method?: string; path: string; snippet?: string; ua?: string; count: number }

type TabKey = 'overview' | 'policy' | 'hits'

// ---- 安全中心（nginx 侧态势与封禁下发） ----
const snap = ref<any>({ status: {}, events: [], topCountries: [], topIps: [], l4: {}, policy: {} })
const pol = reactive<any>({ enabled: true, observe: false, autoBan: false, ccEnabled: false, ccRate: 20, ccBurst: 30, status: 444 })
const blocks = reactive<{ blockIps: any[]; blockCountries: any[] }>({ blockIps: [], blockCountries: [] })
const whitelistText = ref('')
const ipInput = ref('')
const ipMinutes = ref(60)
const savingPolicy = ref(false)

// ---- WAF 规则引擎（日志侧检测与命中日志） ----
const cfg = reactive<{ settings: Settings; rules: Rule[] }>({
  settings: { enabled: true, blockMode: 'observe', autoBan: true, banLadder: [10, 60, 1440], banResetHours: 24, exempt: [] },
  rules: [],
})
const stats = reactive<any>({ lastHour: 0, byAction: {}, topIps: [] })
const form = reactive<Settings>({ enabled: true, blockMode: 'observe', autoBan: true, banLadder: [10, 60, 1440], banResetHours: 24, exempt: [] })
const ladderText = ref('10,60,1440')
const exemptText = ref('')
const savingWaf = ref(false)
const hits = ref<any[]>([])
const hitsLoading = ref(false)
const filter = reactive<{ category?: string; ip: string }>({ category: undefined, ip: '' })

// ---- 页面状态 ----
const loading = ref(true)
const ready = ref(false)
const tab = ref<TabKey>('overview')
const rpsBox = ref<HTMLElement | null>(null)

// ---- 规则测试 / 新增规则 ----
const testOpen = ref(false)
const testing = ref(false)
const testDone = ref(false)
const testHits = ref<any[]>([])
const test = reactive({ method: 'GET', path: '', ua: '' })
const addOpen = ref(false)
const adding = ref(false)
const draft = reactive<{ name: string; pattern: string; scope: string[]; action: string; severity: string }>({
  name: '', pattern: '', scope: ['query'], action: 'observe', severity: 'medium',
})

const catOptions = [
  { value: 'sqli', label: 'SQL 注入' },
  { value: 'xss', label: 'XSS' },
  { value: 'rce', label: '命令执行' },
  { value: 'lfi', label: '路径穿越' },
  { value: 'webshell', label: 'Webshell' },
  { value: 'scanner', label: '扫描器' },
  { value: 'sensitive', label: '敏感路径' },
  { value: 'custom', label: '自定义' },
]
const scopeOptions = [
  { value: 'url', label: '路径' },
  { value: 'query', label: '查询串' },
  { value: 'ua', label: 'UA' },
  { value: 'referer', label: 'Referer' },
  { value: 'any', label: '全部' },
]

const eventCols = [
  { title: '时间', key: 'time', width: 86 },
  { title: '级别', key: 'severity', width: 76 },
  { title: '类型', key: 'kind', width: 96 },
  { title: '来源', key: 'source', width: 150 },
  { title: '详情', key: 'detail', ellipsis: true },
]
const countryCols = [
  { title: '地区', key: 'name', width: 90 },
  { title: '请求', key: 'count', ellipsis: true },
  { title: '', key: 'act', width: 64 },
]
const blockCols = [
  { title: '类型', key: 'kind', width: 72 },
  { title: '值', dataIndex: 'value', ellipsis: true },
  { title: '截至', key: 'until', width: 150 },
  { title: '', key: 'act', width: 60 },
]
const ruleCols = [
  { title: '规则', key: 'name', ellipsis: true },
  { title: '类别', key: 'category', width: 108 },
  { title: '级别', key: 'severity', width: 68 },
  { title: '动作', key: 'action', width: 104 },
  { title: '启用', key: 'enabled', width: 68 },
  { title: '', key: 'act', width: 68 },
]
const hitCols = [
  { title: '时间', key: 'time', width: 150 },
  { title: '命中规则', key: 'rule', width: 200 },
  { title: '来源', key: 'source', width: 150 },
  { title: '请求 / 证据', key: 'path', ellipsis: true },
  { title: '处置', key: 'action', width: 100 },
]

const l4 = computed(() => snap.value.l4 || {})
const healthColor = computed(() => {
  const h = snap.value.health ?? 100
  return h >= 80 ? '#10b981' : h >= 50 ? '#f59e0b' : '#ef4444'
})
const levelText = computed(() => {
  const l = snap.value.level
  return l === 'critical' ? '高风险' : l === 'warn' ? '有异常' : '正常'
})
const enabledRules = computed(() => cfg.rules.filter(r => r.enabled).length)
const hitModeText = computed(() => (cfg.settings.blockMode === 'block' ? '拦截' : '观察'))
const enforceText = computed(() => (!pol.enabled ? '已关闭' : pol.observe ? '观察' : '拦截'))

// 未能真正封禁的原因：按「下发 → 检测」的拦截面从外到内归因，只提示最早挡住的一层
const blockReason = computed(() => {
  if (!pol.enabled) return '安全中心已关闭封禁下发'
  if (pol.observe) return '安全中心处于观察模式'
  if (!cfg.settings.enabled) return 'WAF 总开关已关闭'
  if (cfg.settings.blockMode !== 'block') return 'WAF 命中处置为观察模式'
  return ''
})

// 生效链路三节点：检测 → 自动封禁 → 下发，颜色只表达「生效 / 降级 / 关闭」
const pipeline = computed(() => {
  const s = cfg.settings
  const detect = !s.enabled
    ? { label: '规则检测', text: '已关闭', state: 'off' }
    : { label: '规则检测', text: s.blockMode === 'block' ? '拦截' : '观察', state: s.blockMode === 'block' ? 'on' : 'warn' }
  const auto = !s.autoBan
    ? { label: '自动封禁', text: '关', state: 'off' }
    : { label: '自动封禁', text: '开', state: s.enabled && s.blockMode === 'block' ? 'on' : 'warn' }
  const enforce = !pol.enabled
    ? { label: 'nginx 下发', text: '已关闭', state: 'off' }
    : { label: 'nginx 下发', text: pol.observe ? '观察' : '拦截', state: pol.observe ? 'warn' : 'on' }
  return [detect, auto, enforce]
})

const blockedRows = computed(() => {
  const rows: any[] = []
  const norm = (t: any) => (t && !String(t).startsWith('0001') ? t : '')
  for (const b of blocks.blockIps || []) rows.push({ key: 'ip:' + b.value, kind: 'ip', value: b.value, until: norm(b.until), reason: b.reason })
  for (const b of blocks.blockCountries || []) rows.push({ key: 'cc:' + b.value, kind: 'country', value: b.value, until: norm(b.until), reason: b.reason })
  return rows
})

const catText = (c: string) => catOptions.find(o => o.value === c)?.label || c
const catColor = (c: string) => ({ sqli: 'red', xss: 'volcano', rce: 'magenta', lfi: 'orange', webshell: 'red', scanner: 'blue', sensitive: 'gold', custom: 'purple' } as any)[c] || 'default'
const fmtNum = (v: any) => (v === undefined || v === null ? 0 : Number(v) >= 1000 ? (Number(v) / 1000).toFixed(1) + 'k' : Number(v))
const fmtBps = (v: any) => fmtBytes(Math.round(Number(v) || 0)) + '/s'
const fmtClock = (t: any) => (t ? new Date(t).toLocaleTimeString('zh-CN', { hour12: false }) : '-')
const fmtTime = (t: any) => (t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '-')

// ---- 实时速率图（快照每秒推一帧，只保留近 2 分钟） ----
let chart: any = null
let ro: ResizeObserver | null = null
const series: any[] = [[], []]

const updateChart = () => {
  if (!chart) return
  chart.setOption({
    grid: { left: 44, right: 16, top: 16, bottom: 24 },
    xAxis: { type: 'time', axisLabel: { fontSize: 10, formatter: (v: number) => new Date(v).toLocaleTimeString('zh-CN', { hour12: false }) } },
    yAxis: { type: 'value', axisLabel: { fontSize: 10 } },
    series: [
      { name: 'RPS', type: 'line', data: series[0], showSymbol: false, smooth: true, areaStyle: { opacity: 0.12 }, lineStyle: { color: '#3b82f6' }, itemStyle: { color: '#3b82f6' } },
      { name: '基线', type: 'line', data: series[1], showSymbol: false, smooth: true, lineStyle: { color: '#94a3b8', type: 'dashed', width: 1 }, itemStyle: { color: '#94a3b8' } },
    ],
    tooltip: { trigger: 'axis' },
    legend: { show: false },
  })
}

const pushPoint = (rps: number, base: number) => {
  const t = Date.now()
  series[0].push([t, +rps || 0])
  series[1].push([t, +base || 0])
  for (const s of series) if (s.length > 120) s.shift()
  updateChart()
}

const ensureChart = async () => {
  if (chart || !rpsBox.value) return
  const echarts = await import('echarts')
  chart = echarts.init(rpsBox.value)
  ro = new ResizeObserver(() => {
    if (rpsBox.value && rpsBox.value.clientWidth > 0) chart?.resize()
  })
  ro.observe(rpsBox.value)
  updateChart()
}

// ---- 数据加载 ----
const applyBlocks = (p: any) => {
  if (!p) return
  blocks.blockIps = p.blockIps || []
  blocks.blockCountries = p.blockCountries || []
}
const loadPolicy = async (withDraft = false) => {
  const p = await get<any>('/api/security/policy')
  applyBlocks(p)
  Object.assign(pol, {
    enabled: p.enabled, observe: p.observe, autoBan: p.autoBan,
    ccEnabled: p.ccEnabled, ccRate: p.ccRate, ccBurst: p.ccBurst, status: p.status,
  })
  if (withDraft) whitelistText.value = (p.whitelist || []).join('\n')
}
// 自动封禁/到期回收频繁发生：只同步封禁列表，不动用户正在编辑的策略开关
const refreshBlocks = async () => {
  applyBlocks(await get<any>('/api/security/policy'))
}

const syncForm = () => {
  Object.assign(form, {
    enabled: cfg.settings.enabled,
    blockMode: cfg.settings.blockMode,
    autoBan: cfg.settings.autoBan,
    banLadder: [...(cfg.settings.banLadder || [])],
    banResetHours: cfg.settings.banResetHours,
    exempt: [...(cfg.settings.exempt || [])],
  })
  ladderText.value = (cfg.settings.banLadder || []).join(',')
  exemptText.value = (cfg.settings.exempt || []).join('\n')
}

// 轮询只刷新统计与规则表，不覆盖正在编辑的策略草稿（编辑中的值以表单为准）
const loadWaf = async (withDraft = false) => {
  const r = await getQuiet<any>('/api/waf/view')
  if (!r?.config) return
  Object.assign(cfg.settings, r.config.settings || {})
  cfg.rules = r.config.rules || []
  Object.assign(stats, r.stats || {})
  if (withDraft) syncForm()
}

const loadHits = async (showSpin = false) => {
  if (showSpin) hitsLoading.value = true
  const q: any = { limit: 200 }
  if (filter.category) q.category = filter.category
  if (filter.ip.trim()) q.ip = filter.ip.trim()
  try {
    const r = await getQuiet<any>('/api/waf/hits', q)
    hits.value = (r || []).map((h: Hit, i: number) => ({ ...h, _k: h.time + '-' + h.ruleId + '-' + i }))
  } finally {
    if (showSpin) hitsLoading.value = false
  }
}

// 自动封禁 / 到期回收都发生在后端：快照里的计数变了就同步一次封禁列表（限 3s 一次）
let lastPolicySync = 0
const onSnap = (m: any) => {
  if (!m || !m.time) return
  snap.value = m
  pushPoint(m.rps, m.baselineRps)
  const pv = m.policy || {}
  const want = (pv.blockedIps || 0) + (pv.blockedCountries || 0)
  const have = blocks.blockIps.length + blocks.blockCountries.length
  if (want !== have && Date.now() - lastPolicySync > 3000) {
    lastPolicySync = Date.now()
    refreshBlocks().catch(() => { /* 下一轮快照再试 */ })
  }
}

// 轮询：统计与规则 10s 一次；攻击日志只在日志标签页激活时刷
const pollTick = () => {
  if (!ready.value) return
  loadWaf(false).catch(() => { /* 轮询失败静默 */ })
  if (tab.value === 'hits') loadHits().catch(() => { /* 轮询失败静默 */ })
}
const poll = usePolling(pollTick, 10000, { immediate: false, enabled: () => ready.value })

const firstLoad = async () => {
  await Promise.all([
    loadPolicy(true).catch(() => { /* 首屏失败保持默认 */ }),
    loadWaf(true).catch(() => { /* 首屏失败保持默认 */ }),
    get<any>('/api/security/overview').then(m => { if (m?.time) onSnap(m) }).catch(() => { /* 由 ws 兜底 */ }),
  ])
  loading.value = false
  ready.value = true
  poll.resume()
}

// ---- 生命周期：keep-alive 页面切走即断流，切回重连并补齐快照 ----
let stopWs: (() => void) | null = null

onMounted(() => { ensureChart().catch(() => { /* 图表初始化失败不影响数据 */ }) })

onActivated(async () => {
  if (!ready.value) {
    await firstLoad()
  } else {
    loadPolicy(false).catch(() => { /* 静默 */ })
    get<any>('/api/security/overview').then(m => { if (m?.time) onSnap(m) }).catch(() => { /* 静默 */ })
  }
  stopWs?.()
  stopWs = wsTopic('security', onSnap)
  await nextTick()
  // 页面被 keep-alive 回收后 DOM 重新挂载，图表需要按新尺寸重排；非态势标签下不可见，跳过
  if (tab.value === 'overview') chart?.resize()
})

onDeactivated(() => {
  stopWs?.()
  stopWs = null
})

onUnmounted(() => {
  stopWs?.()
  stopWs = null
  ro?.disconnect()
  chart?.dispose()
  chart = null
})

const onTabChange = (key: string | number) => {
  if (key === 'overview') {
    ensureChart().then(() => nextTick(() => chart?.resize())).catch(() => { /* 忽略 */ })
  } else if (key === 'hits') {
    loadHits(true).catch(() => { /* 已在表格内提示 */ })
  }
}

// ---- 处置动作 ----
const askBlockIp = async (ip: string, reason: string) => {
  if (!ip) { message.warning('请先填写 IP 或 CIDR'); return }
  let warn = ''
  try {
    const r = await post<any>('/api/security/dryrun', { kind: 'ip', value: ip })
    warn = r.warning || ''
  } catch (e: any) {
    message.error(e?.response?.data?.error || '预演失败'); return
  }
  Modal.confirm({
    title: `封禁 ${ip}？`,
    content: `${warn ? warn + '。' : ''}时长 ${ipMinutes.value ? ipMinutes.value + ' 分钟' : '永久'}，可在「安全态势」的「当前封禁」一键解封。`,
    okText: '确认封禁', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      await post('/api/security/block/ip', { value: ip, minutes: ipMinutes.value, reason })
      message.success('已封禁并下发')
      ipInput.value = ''
      await refreshBlocks()
    },
  })
}

const askBlockCountry = async (code: string) => {
  let r: any = {}
  try {
    r = await post<any>('/api/security/dryrun', { kind: 'country', value: code })
  } catch (e: any) {
    message.error(e?.response?.data?.error || '预演失败'); return
  }
  Modal.confirm({
    title: `封禁地区 ${code}？`,
    content: `近 5 分钟 ${r.requests || 0} 次请求，正常请求占 ${(r.okPct || 0).toFixed(1)}%，命中 ${r.matchedCidrs || 0} 条 CIDR。${r.warning || ''}`,
    okText: '确认封禁', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      await post('/api/security/block/country', { value: code, minutes: 60, reason: '手动封禁地区' })
      message.success(`已封禁 ${code}，60 分钟后自动解封`)
      await refreshBlocks()
    },
  })
}

const unblock = async (row: any) => {
  const url = row.kind === 'country' ? '/api/security/unblock/country' : '/api/security/unblock/ip'
  await post(url, { value: row.value })
  message.success('已解封')
  await refreshBlocks()
}

const savePolicy = async () => {
  savingPolicy.value = true
  try {
    const wl = whitelistText.value.split('\n').map(s => s.trim()).filter(Boolean)
    await put('/api/security/policy', {
      ...pol,
      whitelist: wl,
      blockIps: blocks.blockIps,
      blockCountries: blocks.blockCountries,
    })
    message.success('封禁策略已下发 nginx')
    await loadPolicy(true)
  } catch (e: any) {
    message.error(e?.response?.data?.error || '下发失败')
  } finally {
    savingPolicy.value = false
  }
}

// ---- 规则与检测策略 ----
const saveSettings = async () => {
  const ladder = ladderText.value.split(',').map(s => parseInt(s.trim(), 10)).filter(n => !isNaN(n))
  const payload: Settings = {
    ...form,
    banLadder: ladder.length ? ladder : [10, 60, 1440],
    exempt: exemptText.value.split(/[\n,]/).map(s => s.trim()).filter(Boolean),
  }
  savingWaf.value = true
  try {
    await put('/api/waf/settings', payload)
    message.success('检测策略已保存并热生效')
    await loadWaf(true)
  } catch { /* 拦截器已提示 */ } finally { savingWaf.value = false }
}

const updateRule = async (r: Rule, enabled: boolean, action: string) => {
  try {
    await put('/api/waf/rule', { id: r.id, enabled, action })
    await loadWaf(false)
  } catch { /* 已提示 */ }
}

const openAdd = () => {
  draft.name = ''
  draft.pattern = ''
  draft.scope = ['query']
  draft.action = 'observe'
  draft.severity = 'medium'
  addOpen.value = true
}

const submitRule = async () => {
  if (!draft.name.trim() || !draft.pattern.trim()) { message.warning('规则名和正则都要填'); return }
  if (!draft.scope.length) { message.warning('至少选一个作用范围'); return }
  adding.value = true
  try {
    await post('/api/waf/rule', draft)
    message.success('规则已添加并生效')
    addOpen.value = false
    await loadWaf(false)
  } catch { /* 已提示 */ } finally { adding.value = false }
}

const removeRule = (r: Rule) => {
  Modal.confirm({
    title: `删除规则「${r.name}」？`, okType: 'danger', okText: '删除', cancelText: '取消',
    onOk: async () => {
      await del('/api/waf/rule/' + encodeURIComponent(r.id))
      message.success('已删除')
      await loadWaf(false)
    },
  })
}

const askBlock = (ip: string) => {
  Modal.confirm({
    title: `封禁 ${ip}？`,
    content: '封禁 24 小时，可在「安全态势」的「当前封禁」一键解封（安全中心统一管理）。',
    okText: '确认封禁', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      await post('/api/security/block/ip', { value: ip, minutes: 1440, reason: 'WAF 手动封禁' })
      message.success('已封禁并下发 nginx')
      await refreshBlocks()
    },
  })
}

// ---- 规则测试 ----
const openTest = () => {
  testDone.value = false
  testHits.value = []
  testOpen.value = true
}

const fillSample = (kind: string) => {
  const samples: any = {
    sqli: { method: 'GET', path: "/search?q=1%27%20OR%201%3D1--", ua: 'Mozilla/5.0' },
    xss: { method: 'GET', path: '/search?q=%3Cscript%3Ealert(1)%3C/script%3E', ua: 'Mozilla/5.0' },
    lfi: { method: 'GET', path: '/download?file=../../../../etc/passwd', ua: 'Mozilla/5.0' },
    scanner: { method: 'GET', path: '/', ua: 'sqlmap/1.7.2#stable (https://sqlmap.org)' },
  }
  Object.assign(test, samples[kind])
}

const runTest = async () => {
  if (!test.path.trim()) { message.warning('请填写路径'); return }
  testing.value = true
  try {
    const r = await post<any>('/api/waf/test', test)
    testHits.value = r.hits || []
    testDone.value = true
  } catch { /* 已提示 */ } finally { testing.value = false }
}
</script>

<style scoped>
.waf-page { display: flex; flex-direction: column; gap: 12px; }
.banner { margin-bottom: 0; }
/* 指标卡沿用主机页规格：全站同一套字号/高度，合并后两侧数据看起来是一套系统 */
.stat { height: 86px; }
.stat-label { font-size: 12px; color: var(--color-slate); }
.stat-value { font-size: 24px; font-weight: 600; line-height: 1.25; color: var(--color-ink); font-variant-numeric: tabular-nums; }
.unit { font-size: 12px; font-weight: 400; color: var(--color-slate); margin-left: 3px; }
.stat-sub { font-size: 12px; color: var(--color-slate); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.waf-tabs :deep(.ant-tabs-nav) { margin-bottom: 8px; }
.tab-body { display: flex; flex-direction: column; gap: 12px; }
.chart { height: 240px; }
.hint { color: var(--color-slate); font-size: 12px; margin-top: 4px; }
.dim { color: var(--color-slate); font-size: 12px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.cc { margin-left: 4px; }
.pct { color: var(--color-slate); font-size: 12px; }
.detail { margin-right: 8px; }
.mini-act { font-size: 12px; }
.rule-name { font-size: 13px; }
.rule-desc { font-size: 12px; color: var(--color-slate); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 420px; }
.snippet { font-size: 12px; color: #ef4444; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 460px; }
.ellipsis { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.hits-bar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 8px; }
.hits-bar .dim { opacity: 0.75; }
.test-result { margin-top: 16px; }
.test-hit { margin-top: 6px; font-size: 12px; display: flex; gap: 6px; align-items: center; }

/* 生效链路条：把「检测 → 自动封禁 → nginx 下发」画成一条可见的线，
   让两个原本分属不同页面的策略看起来是同一套防护流程 */
.pipeline {
  display: flex; align-items: center; flex-wrap: wrap; gap: 8px;
  padding: 10px 12px; font-size: 12px;
  background: var(--surface-card); border: 1px solid var(--border-card); border-radius: var(--radius-cards);
}
.pipe-title { color: var(--color-slate); font-weight: 500; }
.pipe-node { display: inline-flex; align-items: center; gap: 6px; padding: 2px 9px; border-radius: 999px; background: rgba(128, 128, 128, 0.09); color: var(--color-ink); }
.pipe-dot { width: 6px; height: 6px; border-radius: 50%; background: #94a3b8; display: inline-block; }
.pipe-node.on .pipe-dot { background: #10b981; }
.pipe-node.warn .pipe-dot { background: #f59e0b; }
.pipe-node.off { color: var(--color-slate); }
.pipe-arrow { color: var(--color-slate); }
.pipe-hint { margin-left: auto; color: var(--color-slate); opacity: 0.8; }

@media (max-width: 767px) {
  /* 观察模式横幅在窄屏把操作按钮放到正文下方，避免挤压说明文字 */
  .banner { flex-wrap: wrap; }
  .banner :deep(.ant-alert-action) { margin-inline-start: 0; width: 100%; margin-top: 8px; }
  .banner :deep(.ant-alert-action .ant-btn) { width: 100%; }
  .stat { height: auto; min-height: 76px; }
  .stat-value { font-size: 20px; }
  .chart { height: 200px; }
  .pipe-hint { margin-left: 0; width: 100%; }
}
</style>
