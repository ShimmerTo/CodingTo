<script setup>
import { computed } from 'vue'
import { CheckCircle2, ChevronUp, LoaderCircle } from 'lucide-vue-next'

const props = defineProps({
  items: { type: Array, required: true },
  t: { type: Object, required: true }
})

const currentIndex = computed(() => {
  const index = props.items.findIndex(step => !step.completed)
  return index === -1 ? Math.max(0, props.items.length - 1) : index
})
const current = computed(() => props.items[currentIndex.value] || null)
const allDone = computed(() => props.items.length > 0 && props.items.every(step => step.completed))
</script>

<template>
  <div class="exec-bar">
    <button class="exec-bar__head" type="button" aria-haspopup="true">
      <span class="exec-bar__state">
        <CheckCircle2 v-if="allDone" :size="15" />
        <LoaderCircle v-else class="exec-spin" :size="15" />
      </span>
      <span class="exec-bar__count">{{ allDone ? items.length : currentIndex + 1 }}/{{ items.length }}</span>
      <span class="exec-bar__text">{{ allDone ? (t.planAllDone || '已完成全部计划') : (current?.text || '') }}</span>
      <ChevronUp class="exec-bar__chevron" :size="15" />
    </button>
    <div class="exec-bar__flyout">
      <div class="exec-bar__list">
        <div
          v-for="(item, index) in items"
          :key="index"
          class="exec-item"
          :class="{
            'exec-item--done': item.completed,
            'exec-item--current': index === currentIndex && !allDone && !item.completed
          }"
        >
          <span class="exec-item__mark">
            <CheckCircle2 v-if="item.completed" :size="14" />
            <LoaderCircle v-else-if="index === currentIndex && !allDone" class="exec-spin" :size="14" />
            <span v-else class="exec-item__dot" />
          </span>
          <span class="exec-item__text" :title="item.text">{{ item.text }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped src="../../styles/chat/execution-plan.css"></style>
