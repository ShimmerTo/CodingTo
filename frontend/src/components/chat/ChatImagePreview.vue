<script setup>
import { onBeforeUnmount, onMounted } from 'vue'
import { X } from 'lucide-vue-next'
import { imageSrc } from './chatFormatters.js'

defineProps({
  image: { type: Object, required: true },
  closeTitle: { type: String, default: 'Close' },
  zIndex: { type: Number, default: 1000 }
})

const emit = defineEmits(['close'])

function onKeydown(event) {
  if (event.key === 'Escape') emit('close')
}

onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="image-preview" :style="{ zIndex }" @click.self="emit('close')">
    <button class="image-preview__close" type="button" :title="closeTitle" @click="emit('close')"><X :size="20" /></button>
    <img class="image-preview__img" :src="imageSrc(image)" :alt="image.name" @click.stop />
  </div>
</template>

<style scoped src="../../styles/chat/image-preview.css"></style>
