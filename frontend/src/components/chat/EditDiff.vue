<script setup>
import { computed } from 'vue'
import { toolEditDiffSideBySide } from './chatToolPresentation.js'
import { useAppContext } from '../../composables/appContext.js'

const props = defineProps({
  message: { type: Object, required: true },
  t: { type: Object, required: true }
})

const { config } = useAppContext()

const diff = computed(() => toolEditDiffSideBySide(props.message))
// 代码对比方式：split 左右对照，其余（默认 unified）为上下堆叠。
const isSplit = computed(() => config.preferences.diffMode === 'split')

function lineSign(kind) {
  return kind === 'added' ? '+' : kind === 'deleted' ? '-' : ' '
}
</script>

<template>
  <div v-if="diff" class="tool-edit-diff">
    <section
      v-for="edit in diff.edits"
      :key="`${edit.path}-${edit.index}`"
      class="tool-edit-diff__file"
    >
      <!-- 文件名已在上方 tool-call 标题栏展示，这里不再重复 -->

      <!-- 左右对照（split） -->
      <div v-if="isSplit" class="edit-split">
        <template v-for="(row, ri) in edit.rows" :key="ri">
          <div v-if="row.kind === 'context'" class="edit-split__row is-context">
            <div class="edit-split__cell edit-split__cell--old">
              <span class="edit-split__ln">{{ row.oldNumber ?? '' }}</span>
              <code>{{ row.text }}</code>
            </div>
            <div class="edit-split__cell edit-split__cell--new">
              <span class="edit-split__ln">{{ row.newNumber ?? '' }}</span>
              <code>{{ row.text }}</code>
            </div>
          </div>

          <div v-else class="edit-split__row is-change">
            <div class="edit-split__cell edit-split__cell--old" :class="{ 'is-empty': !row.left }">
              <span class="edit-split__ln">{{ row.left?.num ?? '' }}</span>
              <code>{{ row.left?.text ?? '' }}</code>
            </div>
            <div class="edit-split__cell edit-split__cell--new" :class="{ 'is-empty': !row.right }">
              <span class="edit-split__ln">{{ row.right?.num ?? '' }}</span>
              <code>{{ row.right?.text ?? '' }}</code>
            </div>
          </div>
        </template>
      </div>

      <!-- 上下堆叠（unified） -->
      <div v-else class="edit-unified">
        <div
          v-for="(line, li) in edit.lines"
          :key="li"
          class="edit-unified__row"
          :class="`is-${line.kind}`"
        >
          <span class="edit-unified__ln">{{ line.oldNumber ?? '' }}</span>
          <span class="edit-unified__ln">{{ line.newNumber ?? '' }}</span>
          <span class="edit-unified__sign">{{ lineSign(line.kind) }}</span>
          <code>{{ line.text }}</code>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped src="../../styles/chat/edit-diff.css"></style>
